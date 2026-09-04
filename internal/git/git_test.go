package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"config-mesh/internal/model"
)

func TestStableSnapshotDirectoryIsReused(t *testing.T) {
	deviceID := "0123456789abcdef0123456789abcdef"
	name, err := GenerateStableSnapshotDirName("MacBook-Pro.local", deviceID)
	if err != nil {
		t.Fatal(err)
	}
	if name != "macbook-pro-0123456789ab" {
		t.Fatalf("稳定目录名不符合预期: %s", name)
	}
	repo := t.TempDir()
	first, err := CreateSnapshotStagingDir(repo, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first, "version"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallStagedSnapshot(repo, name, first); err != nil {
		t.Fatal(err)
	}
	second, err := CreateSnapshotStagingDir(repo, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "version"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InstallStagedSnapshot(repo, name, second); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(repo, "hosts", name, "version"))
	if err != nil || string(data) != "two" {
		t.Fatalf("稳定目录没有被原位更新: data=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(repo, "hosts"))
	if err != nil || len(entries) != 1 || entries[0].Name() != name {
		t.Fatalf("重复同步产生了额外目录: entries=%v err=%v", entries, err)
	}
}

func TestCloneFailureIsReturned(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	repoDir := filepath.Join(t.TempDir(), "clone")
	rm, err := NewRepositoryManager(repoDir, missingRemote, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.CloneOrPull(); err == nil || !strings.Contains(err.Error(), "git clone 失败") {
		t.Fatalf("克隆失败必须原样中止，实际: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("失败克隆不得伪装成本地仓库: %v", err)
	}
}

func TestExistingRepoRejectsDifferentRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}
	repoDir := t.TempDir()
	if out, err := exec.Command("git", "init", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "remote", "add", "origin", "https://github.com/alice/one.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %s: %v", out, err)
	}
	rm, err := NewRepositoryManager(repoDir, "https://github.com/alice/two.git", "token", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.CloneOrPull(); err == nil || !strings.Contains(err.Error(), "属于其他远端") {
		t.Fatalf("必须拒绝复用其他仓库目录，实际: %v", err)
	}
}

func TestGitEnvScrubsInheritedConfigAndUsesEphemeralHeader(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "credential.helper")
	t.Setenv("GIT_CONFIG_VALUE_0", "store")
	t.Setenv("GIT_CONFIG_KEY_1", "http.extraHeader")
	t.Setenv("GIT_CONFIG_VALUE_1", "stale-secret")
	t.Setenv("GIT_TRACE_CURL", "1")
	t.Setenv("GIT_CURL_VERBOSE", "1")

	rm := &RepositoryManager{
		RepoURL: "https://github.com/alice/config-mesh-vault.git",
		Token:   "current-secret", AuthUser: "alice",
	}
	env := rm.gitEnv()
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "stale-secret") || strings.Contains(joined, "credential.helper") ||
		strings.Contains(joined, "GIT_TRACE_CURL") || strings.Contains(joined, "GIT_CURL_VERBOSE") {
		t.Fatalf("继承的 Git 配置未清除: %s", joined)
	}
	if !strings.Contains(joined, "GIT_CONFIG_COUNT=1") ||
		!strings.Contains(joined, "GIT_CONFIG_KEY_0=http.extraHeader") ||
		!strings.Contains(joined, "GIT_CONFIG_VALUE_0=Authorization: Basic ") {
		t.Fatalf("未通过临时 Authorization Header 认证: %s", joined)
	}
	if strings.Contains(rm.RepoURL, rm.Token) {
		t.Fatal("Token 不得嵌入仓库 URL")
	}

	nonGitHub := &RepositoryManager{RepoURL: "https://example.com/alice/vault.git", Token: "must-not-send", AuthUser: "alice"}
	if env := strings.Join(nonGitHub.gitEnv(), "\n"); strings.Contains(env, "Authorization: Basic") {
		t.Fatalf("不得把 GitHub Token 发送到其他域名: %s", env)
	}
	redacted := redactAuth("Authorization: Basic YWxpY2U6c2VjcmV0\nraw-secret", "raw-secret")
	if strings.Contains(redacted, "YWxpY2U6c2VjcmV0") || strings.Contains(redacted, "raw-secret") {
		t.Fatalf("认证错误输出脱敏失败: %s", redacted)
	}
}

func TestCommitAndPushIncludesOnlyExplicitManagedPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("未安装 git")
	}
	t.Setenv("HOME", t.TempDir()) // 验证没有用户级 Git identity 时也能提交。
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	repoDir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--bare", remoteDir},
		{"init", repoDir},
		{"-C", repoDir, "remote", "add", "origin", remoteDir},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "recipients.pub"), []byte("managed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "unrelated-secret"), []byte("do not commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repoDir, "add", "unrelated-secret").CombinedOutput(); err != nil {
		t.Fatalf("预暂存无关文件失败: %s: %v", out, err)
	}

	rm, err := NewRepositoryManager(repoDir, remoteDir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.CommitAndPush("managed only", "recipients.pub"); err != nil {
		t.Fatal(err)
	}
	tree, err := exec.Command("git", "-C", repoDir, "ls-tree", "-r", "--name-only", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(tree)) != "recipients.pub" {
		t.Fatalf("提交包含非托管文件: %q", tree)
	}
	author, err := exec.Command("git", "-C", repoDir, "show", "-s", "--format=%ae", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(author)) != "config-mesh@localhost" {
		t.Fatalf("未使用内置提交身份: %q", author)
	}

	// 模拟上一次已经生成本地提交、但 push 因网络失败：重试时工作区是干净的，也必须推送。
	if err := os.WriteFile(filepath.Join(repoDir, "recipients.pub"), []byte("managed retry\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-C", repoDir, "add", "recipients.pub"},
		{"-C", repoDir, "-c", "user.name=test", "-c", "user.email=test@localhost", "commit", "-m", "local ahead", "--", "recipients.pub"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	if err := rm.CommitAndPush("retry push", "recipients.pub"); err != nil {
		t.Fatalf("干净工作区也应重试推送已有本地提交: %v", err)
	}
	localHead, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	remoteHead, err := exec.Command("git", "--git-dir", remoteDir, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(localHead)) != strings.TrimSpace(string(remoteHead)) {
		t.Fatalf("重试没有把本地领先提交推送到远端: local=%s remote=%s", localHead, remoteHead)
	}
}

func TestListHostSnapshotsSortsByMetadataTime(t *testing.T) {
	repo := t.TempDir()
	olderDir := filepath.Join(repo, "hosts", "zeta-20250101")
	newerDir := filepath.Join(repo, "hosts", "alpha-20260901")
	for _, dir := range []string{olderDir, newerDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json.age"), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteSnapshotMetadata(olderDir, model.SnapshotMetadata{
		Version: "v1.0.0", SnapshotID: "zeta-20250101", CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), DeviceID: "0123456789abcdef0123456789abcdef",
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshotMetadata(newerDir, model.SnapshotMetadata{
		Version: "v1.0.0", SnapshotID: "alpha-20260901", CreatedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	snapshots, err := ListHostSnapshots(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 2 || snapshots[0].SnapshotID != "alpha-20260901" {
		t.Fatalf("快照没有按元数据时间排序: %+v", snapshots)
	}
	if snapshots[1].DeviceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("快照没有读取设备 ID: %+v", snapshots[1])
	}
}

func TestListHostSnapshotsIgnoresSymlinkManifest(t *testing.T) {
	repo := t.TempDir()
	snapshotDir := filepath.Join(repo, "hosts", "host-20260901")
	if err := os.MkdirAll(snapshotDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "manifest.json.age")
	if err := os.WriteFile(target, []byte("ciphertext"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(snapshotDir, "manifest.json.age")); err != nil {
		t.Skipf("当前文件系统不支持软链接: %v", err)
	}
	snapshots, err := ListHostSnapshots(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 0 {
		t.Fatalf("软链接 Manifest 不得成为可选快照: %+v", snapshots)
	}
}

func TestListHostSnapshotsIgnoresHiddenAndStagingDirs(t *testing.T) {
	repo := t.TempDir()
	validDir := filepath.Join(repo, "hosts", "host-valid")
	stagingDir := filepath.Join(repo, "hosts", ".staging-temp")
	prevDir := filepath.Join(repo, "hosts", ".previous-old")

	for _, d := range []string{validDir, stagingDir, prevDir} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "manifest.json.age"), []byte("valid-age-data"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	snapshots, err := ListHostSnapshots(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].SnapshotID != "host-valid" {
		t.Fatalf("必须忽略以 . 开头的隐藏/暂存目录，实际: %+v", snapshots)
	}
}

