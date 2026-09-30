package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"config-mesh/internal/crypto"
	"config-mesh/internal/git"
	"config-mesh/internal/github"
	meshlock "config-mesh/internal/lock"
	"config-mesh/internal/model"
	"config-mesh/internal/scanner"
	"config-mesh/internal/state"
	"config-mesh/internal/sync"
	"config-mesh/internal/tui"

	"filippo.io/age"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var repoComponentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var (
	yesFlag      bool
	snapshotFlag string
	downloadFlag bool
	uploadFlag   bool
	strategyFlag string
)

func init() {
	applyCmd.Flags().BoolVarP(&yesFlag, "yes", "y", false, "非交互模式：自动采用默认推荐配置执行同步，跳过 TUI 确认")
	applyCmd.Flags().StringVarP(&snapshotFlag, "snapshot", "s", "", "指定要拉取的云端主机快照 ID (如 xf-macbook-air-13864baa6f97)")
	applyCmd.Flags().BoolVar(&downloadFlag, "download", false, "强制以从云端拉取模式运行 (下载并应用远端配置)")
	applyCmd.Flags().BoolVar(&uploadFlag, "upload", false, "强制以向云端上传模式运行 (备份并推送本机配置)")
	applyCmd.Flags().StringVar(&strategyFlag, "strategy", "", "指定配置合并策略 (overwrite / append / skip)")
}

var applyCmd = &cobra.Command{
	Use:   "apply [github_username]",
	Short: "一键配置网格同步：自动认证、建仓/拉取、扫描挑选、加密上传或解密合并",
	RunE: func(cmd *cobra.Command, args []string) error {
		operationLock, err := meshlock.Acquire()
		if err != nil {
			return err
		}
		defer operationLock.Release()

		var targetUsername string
		if len(args) > 0 {
			targetUsername = strings.TrimSpace(args[0])
		}

		fmt.Println(":: 启动 config-mesh 配置网格同步")

		// 1. GitHub 认证检测
		authMgr := github.NewAuthManager()
		token, err := authMgr.GetToken(targetUsername)
		if err != nil {
			return fmt.Errorf("GitHub 认证失败: %w", err)
		}

		// 2. 初始化 GitHub 客户端并检测用户名与私有仓库
		ghClient := github.NewRepoClient(token)
		authUsername, err := ghClient.GetAuthenticatedUser()
		if err != nil {
			return fmt.Errorf("获取 GitHub 用户信息失败: %w", err)
		}

		if targetUsername == "" {
			targetUsername = authUsername
		}
		if !repoComponentPattern.MatchString(targetUsername) || !repoComponentPattern.MatchString(RepoName) {
			return fmt.Errorf("GitHub owner 或仓库名包含非法字符")
		}

		fmt.Printf(":: 当前 GitHub 用户: %s\n", tui.SelectedStyle.Render(targetUsername))
		fmt.Printf(":: 检查/创建私有仓库 [%s]...\n", tui.SelectedStyle.Render(RepoName))

		repo, err := ghClient.EnsurePrivateRepo(targetUsername, RepoName)
		if err != nil {
			return fmt.Errorf("管理 GitHub 私有仓库失败: %w", err)
		}

		repoURL := repo.GetCloneURL()
		if repoURL == "" {
			repoURL = fmt.Sprintf("https://github.com/%s/%s.git", targetUsername, RepoName)
		}
		fmt.Printf("-> 远程仓库地址: %s\n", repoURL)

		// 3. 确定本地 Git 仓库路径
		repoDir := CustomDir
		if repoDir == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("获取家目录失败: %w", err)
			}
			repoDir = filepath.Join(homeDir, ".config-mesh", "repos", targetUsername, RepoName)
		}

		gitMgr, err := git.NewRepositoryManager(repoDir, repoURL, token, authUsername)
		if err != nil {
			return fmt.Errorf("初始化本地 Git 仓库失败: %w", err)
		}
		gitMgr.DefaultBranch = repo.GetDefaultBranch()

		fmt.Println(":: 同步远端仓库最新状态...")
		if err := gitMgr.CloneOrPull(); err != nil {
			return fmt.Errorf("同步远端仓库失败，已中止后续写入: %w", err)
		}

		// 4. 读取本 vault 的本机身份，用于区分本机稳定槽位和其他设备快照。
		vaultID := targetUsername + "/" + RepoName
		listStateMgr, err := state.NewStateManagerForVault(vaultID)
		if err != nil {
			return err
		}
		listState, err := listStateMgr.LoadState()
		if err != nil {
			return fmt.Errorf("加载本机同步状态失败: %w", err)
		}

		// 5. 探测仓库中是否已存在主机快照
		snapshots, err := git.ListHostSnapshots(repoDir)
		if err != nil {
			return fmt.Errorf("读取快照列表失败: %w", err)
		}

		if downloadFlag && uploadFlag {
			return fmt.Errorf("--download 和 --upload 不能同时指定")
		}
		if strategyFlag != "" {
			validStrategies := map[string]bool{
				string(model.StrategyOverwrite): true,
				string(model.StrategyAppend):    true,
				string(model.StrategySkip):      true,
			}
			if !validStrategies[strings.ToLower(strings.TrimSpace(strategyFlag))] {
				return fmt.Errorf("无效的策略参数: %q (可选: overwrite, append, skip)", strategyFlag)
			}
		}

		defaultDownloadIndex := -1
		options := make([]tui.SnapshotOption, len(snapshots))
		for i, s := range snapshots {
			isLocal := (listState.UploadSnapshotID != "" && s.SnapshotID == listState.UploadSnapshotID) ||
				(listState.DeviceID != "" && s.DeviceID != "" && s.DeviceID == listState.DeviceID)
			ownership := "其他设备"
			if isLocal {
				ownership = "本机"
			} else if defaultDownloadIndex < 0 {
				defaultDownloadIndex = i
			}
			options[i] = tui.SnapshotOption{
				SnapshotID: s.SnapshotID,
				Hostname:   s.Hostname,
				DeviceID:   s.DeviceID,
				Ownership:  ownership,
				CreatedAt:  s.CreatedAt,
			}
		}
		if defaultDownloadIndex < 0 {
			defaultDownloadIndex = 0
		}

		if snapshotFlag != "" {
			found := false
			for _, s := range snapshots {
				if s.SnapshotID == snapshotFlag {
					found = true
					break
				}
			}
			if !found {
				var available []string
				for _, s := range snapshots {
					available = append(available, s.SnapshotID)
				}
				if len(available) == 0 {
					return fmt.Errorf("指定的快照不存在: %q（云端暂无可用快照）", snapshotFlag)
				}
				return fmt.Errorf("指定的快照不存在: %q；云端可用快照: %s", snapshotFlag, strings.Join(available, ", "))
			}
		}

		mode := "upload"
		if snapshotFlag != "" {
			mode = "download"
		} else if downloadFlag {
			mode = "download"
			if len(snapshots) == 0 {
				return fmt.Errorf("云端仓库暂无任何主机快照可供拉取，请先在一台设备上执行同步上传")
			}
		} else if uploadFlag {
			mode = "upload"
		} else if len(snapshots) > 0 {
			// 默认操作判定：若本机未曾上传过或存在其他设备快照，推荐从云端拉取；否则推荐更新上传
			defaultOperation := "upload"
			defaultOpIndex := 1
			hasRemoteSnapshots := len(snapshots) > 0 && defaultDownloadIndex >= 0 && options[defaultDownloadIndex].Ownership == "其他设备"
			if listState.UploadSnapshotID == "" || hasRemoteSnapshots {
				defaultOperation = "download"
				defaultOpIndex = 0
			}

			if !yesFlag {
				isTTY := term.IsTerminal(int(os.Stdin.Fd()))
				if isTTY {
					selectedMode, opErr := tui.RunOperationSelector(":: 请选择本次执行的操作", tui.DefaultOperationOptions(), defaultOpIndex)
					if opErr != nil {
						return opErr
					}
					mode = selectedMode
				} else {
					fmt.Println("\n请选择本次执行的操作：")
					fmt.Println("  1. 从云端拉取配置并合并到当前设备 (推荐用于新设备同步)")
					fmt.Println("  2. 将当前设备配置更新到本机固定目录（仅上传变化项）")
					defNum := "1"
					if defaultOperation == "upload" {
						defNum = "2"
					}
					fmt.Printf("请输入序号 [1/2] (默认 %s): ", defNum)

					scannerInput := bufio.NewScanner(os.Stdin)
					if scannerInput.Scan() {
						input := strings.TrimSpace(scannerInput.Text())
						if input == "2" {
							mode = "upload"
						} else if input == "1" {
							mode = "download"
						} else if input == "" {
							mode = defaultOperation
						} else {
							return fmt.Errorf("无效的操作序号: %q", input)
						}
					}
				}
			} else {
				mode = defaultOperation
			}
		}

		// 6. 分支执行
		if mode == "download" {
			if len(snapshots) == 0 {
				return fmt.Errorf("云端仓库暂无任何主机快照可供拉取")
			}

			selectedSnapshot := ""
			if snapshotFlag != "" {
				selectedSnapshot = snapshotFlag
			} else if !yesFlag {
				isTTY := term.IsTerminal(int(os.Stdin.Fd()))
				if isTTY {
					selectedIndex, selErr := tui.RunSnapshotSelector(":: 请选择要拉取同步的云端主机快照", options, defaultDownloadIndex)
					if selErr != nil {
						return selErr
					}
					selectedSnapshot = snapshots[selectedIndex].SnapshotID
				} else {
					fmt.Printf("\n:: 检测到云端仓库已存在 %d 个主机配置快照:\n", len(snapshots))
					for i, opt := range options {
						created := "时间未知"
						if !opt.CreatedAt.IsZero() {
							created = opt.CreatedAt.Local().Format("2006-01-02 15:04:05")
						}
						hostInfo := ""
						if opt.Hostname != "" {
							hostInfo = fmt.Sprintf(" (主机: %s)", opt.Hostname)
						}
						fmt.Printf("  [%d] [%s] %s%s  (%s)\n", i+1, opt.Ownership, opt.SnapshotID, hostInfo, created)
					}
					fmt.Printf("请选择要拉取的快照 [1-%d] (默认 %d): ", len(snapshots), defaultDownloadIndex+1)
					selectedIndex := defaultDownloadIndex
					scannerInput := bufio.NewScanner(os.Stdin)
					if scannerInput.Scan() {
						input := strings.TrimSpace(scannerInput.Text())
						if input != "" {
							parsed, err := strconv.Atoi(input)
							if err != nil || parsed < 1 || parsed > len(snapshots) {
								return fmt.Errorf("快照序号无效: %q", input)
							}
							selectedIndex = parsed - 1
						}
					}
					selectedSnapshot = snapshots[selectedIndex].SnapshotID
				}
			} else {
				selectedSnapshot = snapshots[defaultDownloadIndex].SnapshotID
				fmt.Printf(":: 自动选择云端快照 (非交互默认): %s\n", selectedSnapshot)
			}

			fmt.Printf(":: 选定同步快照: %s\n", tui.SelectedStyle.Render(selectedSnapshot))
			return handleDownload(repoDir, gitMgr, selectedSnapshot, vaultID, strategyFlag)
		}

		return handleUpload(repoDir, gitMgr, vaultID)
	},
}

// handleUpload 执行本地扫描、勾选、Age 加密并推送到云端仓库
func handleUpload(repoDir string, gitMgr *git.RepositoryManager, vaultID string) error {
	fmt.Println("\n:: 正在扫描本地 macOS 常用开发配置...")
	s, err := scanner.NewScanner()
	if err != nil {
		return err
	}

	items, err := s.Scan()
	if err != nil {
		return fmt.Errorf("配置扫描失败: %w", err)
	}

	// 加载本地历史状态，计算与云端/历史基线的差异
	stateMgr, err := state.NewStateManagerForVault(vaultID)
	if err != nil {
		return err
	}
	localState, err := stateMgr.LoadState()
	if err != nil {
		return err
	}
	items = state.IncludeTrackedMissingItems(items, localState, s.HomeDir)
	items, err = state.AnalyzeDiff(items, nil, localState)
	if err != nil {
		return err
	}

	var selectedItems []model.ConfigItem
	if !yesFlag {
		var err error
		selectedItems, err = tui.RunCheckboxTUIWithDirectoryPreview(":: 选择需要加密并上传同步的本地配置项", items)
		if err != nil {
			return err
		}
	} else {
		selectedItems = items
	}

	var toUpload []model.ConfigItem
	for _, item := range selectedItems {
		if item.Selected && (item.Exists || item.Deleted) {
			toUpload = append(toUpload, item)
		}
	}

	if len(toUpload) == 0 {
		fmt.Println("[*] 未选择任何存在的配置项，上传已取消。")
		return nil
	}

	return uploadConfigItems(repoDir, gitMgr, vaultID, toUpload, selectedItems, "sync: update", false)
}

// autoBackupAndUploadLocal 在下载/合并其他设备配置前，自动扫描并加密备份上传本机现有配置至 GitHub
func autoBackupAndUploadLocal(repoDir string, gitMgr *git.RepositoryManager, vaultID string) error {
	s, err := scanner.NewScanner()
	if err != nil {
		return err
	}

	items, err := s.Scan()
	if err != nil {
		return fmt.Errorf("配置扫描失败: %w", err)
	}

	stateMgr, err := state.NewStateManagerForVault(vaultID)
	if err != nil {
		return err
	}
	localState, err := stateMgr.LoadState()
	if err != nil {
		return err
	}
	items = state.IncludeTrackedMissingItems(items, localState, s.HomeDir)
	items, err = state.AnalyzeDiff(items, nil, localState)
	if err != nil {
		return err
	}

	var toUpload []model.ConfigItem
	for _, item := range items {
		// 自动备份选取本机存在的推荐项
		if item.Exists && item.Recommended {
			item.Selected = true
			toUpload = append(toUpload, item)
		}
	}

	if len(toUpload) == 0 {
		fmt.Println("[*] 本机未发现已有推荐开发配置，跳过云端自动备份上传。")
		return nil
	}

	fmt.Printf("\n:: 检测到本机已有 %d 项开发配置，正在自动备份并加密上传到 GitHub 槽位... \n", len(toUpload))
	return uploadConfigItems(repoDir, gitMgr, vaultID, toUpload, items, "backup: auto-save before download", true)
}

// uploadConfigItems 执行配置打包、Age 加密、写入稳定目录并提交推送到 Git 的通用核心逻辑
func uploadConfigItems(
	repoDir string,
	gitMgr *git.RepositoryManager,
	vaultID string,
	toUpload []model.ConfigItem,
	allScanItems []model.ConfigItem,
	commitPrefix string,
	isAutoBackup bool,
) error {
	// recipients.pub 是 vault 的多设备接收者注册表。首次上传时用一把本地完整密钥对建立。
	recipientsPath := filepath.Join(repoDir, "recipients.pub")
	keyPairs, discoverErr := crypto.DiscoverLocalSSHKeys()
	if _, err := os.Stat(recipientsPath); os.IsNotExist(err) {
		if discoverErr != nil {
			return discoverErr
		}
		var chosen *model.KeyPairInfo
		for i := range keyPairs {
			if keyPairs[i].PrivKeyPath != "" {
				chosen = &keyPairs[i]
				break
			}
		}
		if chosen == nil {
			return fmt.Errorf("未在 ~/.ssh 中找到同时具有公钥和私钥的 ed25519/rsa 密钥对")
		}
		fingerprint, _, err := crypto.AddRecipient(recipientsPath, chosen.PubKeyPath)
		if err != nil {
			return fmt.Errorf("初始化 recipients.pub 失败: %w", err)
		}
		fmt.Printf("[key] 已建立初始接收者: %s (%s)\n", chosen.Name, fingerprint)
	} else if discoverErr == nil {
		// 如果 recipients.pub 已存在，但当前设备有可用的 SSH 公钥，且尚未加入 recipients.pub，则自动加入
		for i := range keyPairs {
			if keyPairs[i].PubKeyPath != "" {
				if fingerprint, added, err := crypto.AddRecipient(recipientsPath, keyPairs[i].PubKeyPath); err == nil && added {
					fmt.Printf("[key] 已自动将本机 SSH 接收者加入 recipients.pub: %s (%s)\n", keyPairs[i].Name, fingerprint)
				}
				break
			}
		}
	}
	recipients, err := crypto.LoadRecipients(recipientsPath)
	if err != nil {
		return fmt.Errorf("加载多设备接收者失败: %w", err)
	}
	if !isAutoBackup {
		fmt.Printf("[key] 将为 %d 个已授权设备接收者加密。\n", len(recipients))
	}
	recipientsHash, err := crypto.CalculateFileSHA256(recipientsPath)
	if err != nil {
		return fmt.Errorf("计算接收者注册表哈希失败: %w", err)
	}

	// 每台设备持久化一个随机 ID，并长期复用一个稳定仓库目录。
	stateMgr, err := state.NewStateManagerForVault(vaultID)
	if err != nil {
		return err
	}
	localState, err := stateMgr.LoadState()
	if err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	deviceID, err := stateMgr.EnsureDeviceID(localState)
	if err != nil {
		return err
	}
	snapshotName := localState.UploadSnapshotID
	if snapshotName == "" {
		snapshotName, err = git.GenerateStableSnapshotDirName(hostname, deviceID)
		if err != nil {
			return err
		}
		localState.UploadSnapshotID = snapshotName
		if err := stateMgr.SaveState(localState); err != nil {
			return fmt.Errorf("保存本机稳定上传目录失败: %w", err)
		}
	}
	snapshotDir := filepath.Join(repoDir, "hosts", snapshotName)

	var previousManifest *model.Manifest
	if info, statErr := os.Lstat(snapshotDir); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("本机稳定上传路径不是安全目录: %s", snapshotDir)
		}
		previousManifest, err = loadPreviousUploadManifest(snapshotDir, snapshotName, deviceID)
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("检查本机稳定上传目录失败: %w", statErr)
	}

	if err := verifyItemsUnchangedSinceScan(toUpload); err != nil {
		return err
	}
	if uploadSnapshotUnchanged(toUpload, previousManifest, recipientsHash, snapshotDir) {
		previousByID := manifestItemsByID(previousManifest)
		uploadedStateItems := make([]model.ConfigItem, 0, len(toUpload))
		for _, item := range toUpload {
			item = canonicalUploadItem(item)
			if previous, ok := previousByID[item.ID]; ok {
				item.PayloadHash = previous.PayloadHash
			}
			item.RemoteHash = item.ContentHash
			uploadedStateItems = append(uploadedStateItems, item)
		}
		// 不创建空提交，但仍执行 push，以恢复“上次本地提交成功、远端推送失败”的情况。
		if err := gitMgr.CommitAndPush("sync: verify "+snapshotName, "recipients.pub", filepath.ToSlash(filepath.Join("hosts", snapshotName))); err != nil {
			return fmt.Errorf("确认仓库同步状态失败: %w", err)
		}
		if err := stateMgr.RecordUploadSuccess(allScanItems, uploadedStateItems, snapshotName, recipientsHash); err != nil {
			return fmt.Errorf("配置内容未变化，但更新本地同步状态失败: %w", err)
		}
		if isAutoBackup {
			fmt.Printf("[ok] 本机配置已在云端备份 (内容无变化，槽位: %s)\n", snapshotName)
		} else {
			fmt.Printf("\n[ok] 本机 %d 项配置与仓库一致 (未创建新目录、未重复加密、未生成空提交)\n", len(toUpload))
			fmt.Printf("[*] 本机同步状态已更新: %s\n\n", stateMgr.StateFilePath)
		}
		return nil
	}

	createdAt := time.Now()
	stagingDir, err := git.CreateSnapshotStagingDir(repoDir, snapshotName)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagingDir)
	vaultDir := filepath.Join(stagingDir, "vault")

	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		return fmt.Errorf("创建快照存储目录失败: %w", err)
	}

	fmt.Printf(":: 正在更新本机稳定配置目录: %s\n", snapshotName)

	var uploadedManifestItems []model.ConfigItem
	var uploadedStateItems []model.ConfigItem
	previousByID := manifestItemsByID(previousManifest)
	recipientsUnchanged := previousManifest != nil && previousManifest.RecipientsHash == recipientsHash
	reusedCount := 0
	encryptedCount := 0

	for _, item := range toUpload {
		item = canonicalUploadItem(item)
		if item.Deleted {
			uploadedStateItems = append(uploadedStateItems, item)
			manifestItem := item
			manifestItem.LocalPath = ""
			manifestItem.Selected = false
			manifestItem.DiffStatus = ""
			uploadedManifestItems = append(uploadedManifestItems, manifestItem)
			continue
		}
		beforeHash, err := state.CalculateItemHash(item)
		if err != nil {
			return fmt.Errorf("打包前计算配置哈希失败 (%s): %w", item.Name, err)
		}
		if beforeHash == "" || beforeHash != item.ContentHash {
			return fmt.Errorf("配置在选择后发生变化，请重试: %s", item.Name)
		}

		previous, hasPrevious := previousByID[item.ID]
		if hasPrevious && canReuseEncryptedPayload(item, previous, recipientsUnchanged, snapshotDir) {
			item.PayloadHash = previous.PayloadHash
			source := filepath.Join(snapshotDir, "vault", previous.VaultFile)
			destination := filepath.Join(vaultDir, item.VaultFile)
			if err := copyEncryptedPayload(source, destination); err != nil {
				return fmt.Errorf("复用已有加密负载失败 (%s): %w", item.Name, err)
			}
			reusedCount++
		} else {
			var plainData []byte
			if item.IsDir {
				data, err := sync.CreateTarArchive(item.LocalPath)
				if err != nil {
					return fmt.Errorf("打包目录失败 (%s): %w", item.Name, err)
				}
				plainData = data
			} else {
				data, err := readRegularFileBounded(item.LocalPath, sync.MaxArchiveBytes)
				if err != nil {
					return fmt.Errorf("读取文件失败 (%s): %w", item.Name, err)
				}
				plainData = data
			}
			if err := sync.ValidateSensitivePayload(item, plainData); err != nil {
				return fmt.Errorf("配置负载校验失败 (%s): %w", item.Name, err)
			}
			if int64(len(plainData)) > sync.MaxArchiveBytes {
				return fmt.Errorf("配置负载超过 %d MiB 安全上限: %s", sync.MaxArchiveBytes>>20, item.Name)
			}

			item.PayloadHash = crypto.CalculateSHA256(plainData)
			cipherData, err := crypto.Encrypt(plainData, recipients...)
			if err != nil {
				return fmt.Errorf("加密文件失败 (%s): %w", item.Name, err)
			}
			destVaultPath := filepath.Join(vaultDir, item.VaultFile)
			if err := os.WriteFile(destVaultPath, cipherData, 0644); err != nil {
				return fmt.Errorf("写入加密文件失败: %w", err)
			}
			encryptedCount++
		}

		afterHash, err := state.CalculateItemHash(item)
		if err != nil {
			return fmt.Errorf("打包后计算配置哈希失败 (%s): %w", item.Name, err)
		}
		if beforeHash == "" || beforeHash != afterHash {
			return fmt.Errorf("配置在打包期间发生变化，请重试: %s", item.Name)
		}
		item.ContentHash = afterHash

		item.RemoteHash = item.ContentHash
		uploadedStateItems = append(uploadedStateItems, item)
		manifestItem := item
		manifestItem.LocalPath = ""
		manifestItem.Exists = false
		manifestItem.Selected = false
		manifestItem.RemoteHash = ""
		manifestItem.DiffStatus = ""
		uploadedManifestItems = append(uploadedManifestItems, manifestItem)
	}

	// 生成 Manifest 并加密存储
	manifest := model.Manifest{
		Version:        Version,
		Hostname:       hostname,
		SnapshotID:     snapshotName,
		CreatedAt:      createdAt,
		Items:          uploadedManifestItems,
		DeviceID:       deviceID,
		RecipientsHash: recipientsHash,
	}
	if localState.RemoteSnapshotID != snapshotName {
		manifest.ParentID = localState.RemoteSnapshotID
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Manifest 失败: %w", err)
	}
	cipherManifest, err := crypto.Encrypt(manifestBytes, recipients...)
	if err != nil {
		return fmt.Errorf("加密 Manifest 失败: %w", err)
	}

	if err := os.WriteFile(filepath.Join(stagingDir, "manifest.json.age"), cipherManifest, 0644); err != nil {
		return fmt.Errorf("写入加密 Manifest 失败: %w", err)
	}
	if err := git.WriteSnapshotMetadata(stagingDir, model.SnapshotMetadata{
		Version: Version, SnapshotID: snapshotName, Hostname: hostname, CreatedAt: createdAt, DeviceID: deviceID,
	}); err != nil {
		return fmt.Errorf("写入快照排序元数据失败: %w", err)
	}
	if err := git.InstallStagedSnapshot(repoDir, snapshotName, stagingDir); err != nil {
		return err
	}

	// 提交并推送到 GitHub 私有仓库
	fmt.Println(":: 正在提交并推送到 GitHub 私有仓库...")
	commitMsg := fmt.Sprintf("%s %s (%d encrypted, %d reused)", commitPrefix, snapshotName, encryptedCount, reusedCount)
	if err := gitMgr.CommitAndPush(commitMsg, "recipients.pub", filepath.ToSlash(filepath.Join("hosts", snapshotName))); err != nil {
		return fmt.Errorf("推送至 GitHub 失败: %w", err)
	}

	// 保存完整选择表、每项哈希与同步时间到当前 vault 的隔离状态库。
	if err := stateMgr.RecordUploadSuccess(allScanItems, uploadedStateItems, snapshotName, recipientsHash); err != nil {
		return fmt.Errorf("快照已推送，但更新本地状态失败: %w", err)
	}

	var totalUploadedSize int64
	for _, item := range uploadedManifestItems {
		totalUploadedSize += item.Size
	}

	if isAutoBackup {
		fmt.Printf("[ok] 本机现有配置已自动备份并上传至云端槽位 [%s] (%d 项加密，%d 项复用，总计 %s)\n",
			snapshotName, encryptedCount, reusedCount, scanner.FormatSize(totalUploadedSize))
	} else {
		fmt.Printf("\n[ok] 同步成功！稳定目录 [%s] 已更新: %d 项重新加密, %d 项复用, 总计 %s\n",
			snapshotName, encryptedCount, reusedCount, scanner.FormatSize(totalUploadedSize))
		fmt.Printf("[*] 本机同步状态: %s\n", stateMgr.StateFilePath)
		fmt.Println("[*] 在另一台 Mac 设备上执行相同命令即可解密同步:")
		fmt.Printf("   config-mesh apply %s\n\n", gitMgr.AuthUser)
	}

	return nil
}

// handleDownload 从指定快照挑选配置、前置备份、解密并按策略合并
func handleDownload(repoDir string, gitMgr *git.RepositoryManager, snapshotName string, vaultID string, strategyOverride string) error {
	if snapshotName == "" {
		return fmt.Errorf("未选择任何远端快照")
	}
	snapshotDir := filepath.Join(repoDir, "hosts", snapshotName)
	manifestPath := filepath.Join(snapshotDir, "manifest.json.age")

	cipherManifest, err := readRegularFileBounded(manifestPath, 4<<20)
	if err != nil {
		return fmt.Errorf("读取远端快照元数据失败: %w", err)
	}

	// 尝试所有本地身份，而不是见到第一把可解析私钥就停止。
	identities, plainManifestBytes, err := decryptManifestWithLocalKeys(cipherManifest)
	if err != nil {
		return err
	}

	var remoteManifest model.Manifest
	if err := json.Unmarshal(plainManifestBytes, &remoteManifest); err != nil {
		return fmt.Errorf("解析清单失败: %w", err)
	}
	if remoteManifest.Version != Version {
		return fmt.Errorf("快照版本不兼容: %s（当前客户端 %s）", remoteManifest.Version, Version)
	}
	if remoteManifest.SnapshotID != snapshotName {
		return fmt.Errorf("manifest 快照 ID 与目录不匹配: %s != %s", remoteManifest.SnapshotID, snapshotName)
	}
	metadataPath := filepath.Join(snapshotDir, "snapshot-meta.json")
	if metadataBytes, readErr := readRegularFileBounded(metadataPath, 1<<20); readErr == nil {
		var metadata model.SnapshotMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			return fmt.Errorf("解析快照排序元数据失败: %w", err)
		}
		if metadata.SnapshotID != remoteManifest.SnapshotID || metadata.Hostname != remoteManifest.Hostname ||
			!metadata.CreatedAt.Equal(remoteManifest.CreatedAt) ||
			(metadata.DeviceID != "" && metadata.DeviceID != remoteManifest.DeviceID) {
			return fmt.Errorf("公开快照元数据与加密 manifest 不一致")
		}
	} else if !os.IsNotExist(readErr) {
		return fmt.Errorf("读取快照排序元数据失败: %w", readErr)
	}
	if len(remoteManifest.Items) > 10_000 {
		return fmt.Errorf("manifest 配置项数过多: %d", len(remoteManifest.Items))
	}

	// 填充本地对应文件的状态并执行差异分析
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	seenIDs := make(map[string]bool)
	seenVaultFiles := make(map[string]bool)
	for i := range remoteManifest.Items {
		item, err := sync.ValidateAndResolveConfigItem(homeDir, remoteManifest.Items[i])
		if err != nil {
			return fmt.Errorf("manifest 第 %d 项校验失败: %w", i+1, err)
		}
		if seenIDs[item.ID] || (!item.Deleted && seenVaultFiles[item.VaultFile]) {
			return fmt.Errorf("manifest 包含重复 ID 或 vault 文件: %s", item.ID)
		}
		seenIDs[item.ID] = true
		if !item.Deleted {
			seenVaultFiles[item.VaultFile] = true
		}
		_, statErr := os.Lstat(item.LocalPath)
		item.Exists = statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			return fmt.Errorf("检查本地配置失败 (%s): %w", item.LocalPath, statErr)
		}
		remoteManifest.Items[i] = item
	}

	stateMgr, err := state.NewStateManagerForVault(vaultID)
	if err != nil {
		return err
	}
	localState, err := stateMgr.LoadState()
	if err != nil {
		return err
	}
	remoteManifest.Items, err = state.AnalyzeDiff(remoteManifest.Items, &remoteManifest, localState)
	if err != nil {
		return err
	}

	var toApply []model.ConfigItem
	var strategy model.Strategy
	if !yesFlag {
		// 1. TUI 让用户勾选挑选想要同步的配置项
		selectedItems, err := tui.RunCheckboxTUI(":: 请勾选想要从云端同步到本机的配置项", remoteManifest.Items)
		if err != nil {
			return err
		}

		for _, item := range selectedItems {
			if item.Selected {
				toApply = append(toApply, item)
			}
		}

		if len(toApply) == 0 {
			fmt.Println("[*] 未选择任何配置项，同步已取消。")
			return nil
		}

		// 2. 先选择策略并验证所有项是否支持，取消不得默认改写配置。
		if strategyOverride != "" {
			strategy = model.Strategy(strings.ToLower(strings.TrimSpace(strategyOverride)))
		} else {
			strategy, err = tui.RunStrategySelector()
			if err != nil {
				return err
			}
			if strategy == model.StrategySkip {
				fmt.Println("[*] 已选择跳过，本地配置和同步状态均未修改。")
				return nil
			}
		}
	} else {
		for _, item := range remoteManifest.Items {
			if item.Recommended {
				toApply = append(toApply, item)
			}
		}
		if len(toApply) == 0 {
			fmt.Println("[*] 未选择任何配置项，同步已取消。")
			return nil
		}
		if strategyOverride != "" {
			strategy = model.Strategy(strings.ToLower(strings.TrimSpace(strategyOverride)))
		} else {
			strategy = model.StrategyOverwrite
		}
		fmt.Printf(":: 应用策略 (非交互默认): %s\n", strategy)
	}
	for _, item := range toApply {
		if err := sync.ValidateApplyStrategy(item, strategy); err != nil {
			return err
		}
	}
	fmt.Printf(":: 应用策略: %s\n", strategy)

	// 3. 在任何本地写入前，先完整解密并验证所有勾选项。
	vaultDir := filepath.Join(snapshotDir, "vault")
	payloads := make([]decryptedConfig, 0, len(toApply))
	for _, item := range toApply {
		if item.Deleted {
			payloads = append(payloads, decryptedConfig{item: item})
			continue
		}
		vaultFile := filepath.Join(vaultDir, item.VaultFile)
		cipherData, err := readRegularFileBounded(vaultFile, sync.MaxArchiveBytes+(1<<20))
		if err != nil {
			return fmt.Errorf("读取加密文件失败 (%s): %w", item.VaultFile, err)
		}

		plainData, err := crypto.Decrypt(cipherData, identities...)
		if err != nil {
			return fmt.Errorf("解密配置失败 (%s): %w", item.Name, err)
		}
		if int64(len(plainData)) > sync.MaxArchiveBytes {
			return fmt.Errorf("解密负载超过 %d MiB 安全上限: %s", sync.MaxArchiveBytes>>20, item.Name)
		}
		if item.PayloadHash != "" && crypto.CalculateSHA256(plainData) != item.PayloadHash {
			return fmt.Errorf("配置负载哈希与 Manifest 不匹配: %s", item.Name)
		}
		if item.IsDir {
			if err := sync.ValidateTarArchive(plainData); err != nil {
				return fmt.Errorf("目录归档验证失败 (%s): %w", item.Name, err)
			}
		} else if err := sync.ValidateSensitivePayload(item, plainData); err != nil {
			return fmt.Errorf("配置负载校验失败 (%s): %w", item.Name, err)
		}
		payloads = append(payloads, decryptedConfig{item: item, data: plainData})
	}

	// 4. 备份包含“原本不存在”记录，使回滚可删除本次新建项，同时备份 LocalState 状态库基线。
	bm, err := sync.NewBackupManager()
	if err != nil {
		return err
	}
	bm.StateFilePath = stateMgr.StateFilePath
	bm.VaultID = vaultID
	backupManifest, err := bm.BackupSelectedFiles(toApply)
	if err != nil {
		return fmt.Errorf("前置快照备份失败: %w", err)
	}
	existingCount := 0
	for _, item := range backupManifest.Items {
		if item.Exists {
			existingCount++
		}
	}
	fmt.Printf("[ok] 已建立本地前置回滚快照 (%d 项已有，%d 项原本不存在): %s\n",
		existingCount, len(backupManifest.Items)-existingCount, backupManifest.BackupDir)

	// 5. 自动将当前设备本地现有配置加密备份并上传至 GitHub 私有仓库
	if err := autoBackupAndUploadLocal(repoDir, gitMgr, vaultID); err != nil {
		return fmt.Errorf("云端自动前置备份失败，已中止下载以防本地配置丢失: %w", err)
	}

	// 6. 任一应用失败立即恢复前置快照。
	appliedItems := make([]model.ConfigItem, 0, len(payloads))
	for _, payload := range payloads {
		if err := sync.ApplyConfigItem(payload.item, payload.data, strategy); err != nil {
			rollbackErr := bm.Rollback(backupManifest.BackupID)
			if rollbackErr != nil {
				return fmt.Errorf("应用失败 (%s): %v；自动回滚也失败: %w", payload.item.Name, err, rollbackErr)
			}
			return fmt.Errorf("应用失败 (%s)，已自动回滚: %w", payload.item.Name, err)
		}
		item := payload.item
		item.Exists = !item.Deleted
		item.Strategy = strategy
		item.Selected = true
		appliedItems = append(appliedItems, item)
		fmt.Printf("  [ok] 已同步: %s\n", item.Name)
	}

	if err := stateMgr.RecordDownloadSuccess(appliedItems, snapshotName); err != nil {
		rollbackErr := bm.Rollback(backupManifest.BackupID)
		if rollbackErr != nil {
			return fmt.Errorf("更新同步状态失败: %v；自动回滚也失败: %w", err, rollbackErr)
		}
		return fmt.Errorf("更新同步状态失败，配置已自动回滚: %w", err)
	}

	var totalAppliedSize int64
	for _, item := range appliedItems {
		totalAppliedSize += item.Size
	}

	fmt.Printf("\n[ok] 同步应用完成！共更新 %d 项配置 (总计大小: %s)\n", len(appliedItems), scanner.FormatSize(totalAppliedSize))
	fmt.Println("[*] 若需要撤销本次同步，可随时执行一键回滚:")
	fmt.Printf("   config-mesh rollback %s\n\n", backupManifest.BackupID)

	return nil
}

type decryptedConfig struct {
	item model.ConfigItem
	data []byte
}

func readRegularFileBounded(filePath string, maxBytes int64) ([]byte, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("拒绝读取非普通文件: %s", filePath)
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return nil, fmt.Errorf("文件超过 %d 字节上限: %s", maxBytes, filePath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("文件读取期间增长并超过 %d 字节上限: %s", maxBytes, filePath)
	}
	return data, nil
}

func decryptManifestWithLocalKeys(cipherManifest []byte) ([]age.Identity, []byte, error) {
	keyPairs, err := crypto.DiscoverLocalSSHKeys()
	if err != nil {
		return nil, nil, fmt.Errorf("发现本地 SSH 密钥失败: %w", err)
	}
	var identities []age.Identity
	var encryptedKeys []model.KeyPairInfo
	for _, kp := range keyPairs {
		if kp.PrivKeyPath == "" {
			continue
		}
		identity, parseErr := crypto.ParseSSHPrivateKey(kp.PrivKeyPath, "")
		if parseErr == nil {
			identities = append(identities, identity)
			continue
		}
		if crypto.IsPassphraseRequired(parseErr) {
			encryptedKeys = append(encryptedKeys, kp)
		}
	}

	if len(identities) > 0 {
		if plain, decryptErr := crypto.Decrypt(cipherManifest, identities...); decryptErr == nil {
			fmt.Printf("[key] 已从 %d 个本地 SSH 身份中找到匹配密钥\n", len(identities))
			return identities, plain, nil
		}
	}

	for _, kp := range encryptedKeys {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, nil, fmt.Errorf("SSH 私钥 %s 已加密，非交互终端无法安全读取密码", kp.Name)
		}
		fmt.Printf("[key] 请输入 SSH 私钥 %s 的密码: ", kp.Name)
		secret, readErr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if readErr != nil {
			return nil, nil, fmt.Errorf("读取 SSH 私钥密码失败: %w", readErr)
		}
		identity, parseErr := crypto.ParseSSHPrivateKey(kp.PrivKeyPath, string(secret))
		for i := range secret {
			secret[i] = 0
		}
		if parseErr != nil {
			fmt.Printf("[!] 无法解锁 %s，继续尝试其他密钥\n", kp.Name)
			continue
		}
		identities = append(identities, identity)
		if plain, decryptErr := crypto.Decrypt(cipherManifest, identities...); decryptErr == nil {
			return identities, plain, nil
		}
	}

	if len(keyPairs) == 0 {
		return nil, nil, fmt.Errorf("未在 ~/.ssh 中找到可用的 SSH 密钥对")
	}
	return nil, nil, fmt.Errorf("没有任何本地 SSH 私钥能够解密该快照；请先在已授权设备上将本机公钥加入 recipients.pub")
}
