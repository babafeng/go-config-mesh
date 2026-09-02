package model

import "time"

// Strategy 表示配置冲突时的合并策略
type Strategy string

// SecretKind 标识被明确允许进入加密快照的凭据类型。
// 非空值必须同时通过路径白名单和负载格式校验。
type SecretKind string

const (
	StrategyAppend    Strategy = "append"    // 追加（纯文本使用标记块保护，结构化配置做 Merge）
	StrategyOverwrite Strategy = "overwrite" // 覆盖（全量覆盖本地文件）
	StrategyReplace   Strategy = "replace"   // 替换（类似覆盖）
	StrategySkip      Strategy = "skip"      // 跳过（不修改本地文件）
)

const (
	SecretKindSSHPrivateKey  SecretKind = "ssh_private_key"
	SecretKindAWSCredentials SecretKind = "aws_credentials"
	SecretKindAliyunConfig   SecretKind = "aliyun_config"
	// SecretKindDetectedConfig 表示预设配置文件中检测到了凭据内容。
	// 这类文件仍可加密同步，但会进入凭据确认流程并强制使用 0600 权限。
	SecretKindDetectedConfig SecretKind = "detected_config_secret"
)

// ConfigCategory 配置分类
type ConfigCategory string

const (
	CategoryAI      ConfigCategory = "AI 助手 & CLI"
	CategoryCloud   ConfigCategory = "云平台 & DevOps"
	CategoryShell   ConfigCategory = "Shell & 终端"
	CategoryGit     ConfigCategory = "Git & 开发"
	CategorySSH     ConfigCategory = "SSH 配置"
	CategoryEditor  ConfigCategory = "编辑器 & IDE"
	CategoryTools   ConfigCategory = "开发工具 & 终端模拟器"
	CategoryPackage ConfigCategory = "包管理 & 语言环境"
	CategoryCustom  ConfigCategory = "自定义配置"
)

// ConfigItem 单个配置项模型
type ConfigItem struct {
	ID          string         `json:"id"`                     // 唯一标识 (如 shell_zshrc)
	Name        string         `json:"name"`                   // 显示名称 (如 ~/.zshrc)
	Category    ConfigCategory `json:"category"`               // 分类
	LocalPath   string         `json:"local_path"`             // 本地绝对路径 (如 /Users/xxx/.zshrc)
	RelHomePath string         `json:"rel_home_path"`          // 相对于家目录的路径 (如 .zshrc)
	VaultFile   string         `json:"vault_file"`             // 仓库中的加密文件名 (如 shell_zshrc.age)
	IsDir       bool           `json:"is_dir"`                 // 是否为目录 (如 ~/.config/nvim)
	Recommended bool           `json:"recommended"`            // 是否默认推荐勾选
	Exists      bool           `json:"exists"`                 // 本地是否存在
	Selected    bool           `json:"selected"`               // 是否被用户选中
	Strategy    Strategy       `json:"strategy,omitempty"`     // 解决冲突策略 (应用时使用)
	FileMode    uint32         `json:"file_mode,omitempty"`    // 文件权限 (如 0644)
	ContentHash string         `json:"content_hash,omitempty"` // 本地 SHA256 哈希值
	PayloadHash string         `json:"payload_hash,omitempty"` // 加密前负载的 SHA256，用于绑定 Manifest 与密文
	RemoteHash  string         `json:"remote_hash,omitempty"`  // 云端 SHA256 哈希值
	DiffStatus  DiffStatus     `json:"diff_status,omitempty"`  // 差异对比状态 (synced, modified, etc.)
	Size        int64          `json:"size,omitempty"`         // 文件大小 (字节)
	Deleted     bool           `json:"deleted,omitempty"`      // 显式删除 tombstone，与“未选中”区分
	SecretKind  SecretKind     `json:"secret_kind,omitempty"`  // 受控敏感类型；显式凭据默认不选，检测型配置保留推荐状态
}

// Manifest 仓库快照元数据清单
type Manifest struct {
	Version        string       `json:"version"`                   // config-mesh 版本
	Hostname       string       `json:"hostname"`                  // 主机名
	SnapshotID     string       `json:"snapshot_id"`               // 稳定设备槽位标识 (如 macbook-pro-a1b2c3d4e5f6)
	CreatedAt      time.Time    `json:"created_at"`                // 槽位最近内容更新时间
	Items          []ConfigItem `json:"items"`                     // 配置项列表
	EncryptedKey   string       `json:"encrypted_key,omitempty"`   // 可选的主机加密密钥槽
	ParentID       string       `json:"parent_id,omitempty"`       // 创建快照时本机所观察的父快照
	DeviceID       string       `json:"device_id,omitempty"`       // 创建该稳定快照槽位的本机随机标识
	RecipientsHash string       `json:"recipients_hash,omitempty"` // 加密接收者注册表哈希；变化时需重新加密
}

// BackupManifest 本地前置快照备份元数据清单
type BackupManifest struct {
	BackupID  string       `json:"backup_id"`  // 备份标识 (如 backup-20260901-150405)
	CreatedAt time.Time    `json:"created_at"` // 备份时间
	Hostname  string       `json:"hostname"`   // 本机主机名
	BackupDir string       `json:"backup_dir"` // 本地备份存储目录
	Items     []ConfigItem `json:"items"`      // 备份的配置项清单
}

// DiffStatus 配置项差异对比状态
type DiffStatus string

const (
	DiffStatusNew            DiffStatus = "new"            // 本地新发现/未曾同步
	DiffStatusSynced         DiffStatus = "synced"         // 本地与云端完全一致
	DiffStatusLocalModified  DiffStatus = "local_modified" // 本地有修改（待推送）
	DiffStatusRemoteModified DiffStatus = "remote_updated" // 远端有更新（待拉取）
	DiffStatusConflict       DiffStatus = "conflict"       // 本地与远端均有修改（冲突）
)

// KeyPairInfo 本地发现的 SSH 密钥对信息
type KeyPairInfo struct {
	Name        string `json:"name"`          // 密钥文件名 (如 id_ed25519.pub)
	PubKeyPath  string `json:"pub_key_path"`  // 公钥绝对路径
	PrivKeyPath string `json:"priv_key_path"` // 对应私钥绝对路径 (若存在)
	KeyType     string `json:"key_type"`      // 密钥类型 (ssh-ed25519, ssh-rsa)
	Fingerprint string `json:"fingerprint"`   // 指纹
}

// TrackedItemState 记录单个配置项在本地状态中的基线快照
type TrackedItemState struct {
	ID             string         `json:"id"`
	Name           string         `json:"name,omitempty"`
	RelHomePath    string         `json:"rel_home_path"`
	VaultFile      string         `json:"vault_file,omitempty"`
	Category       ConfigCategory `json:"category,omitempty"`
	IsDir          bool           `json:"is_dir,omitempty"`
	Deleted        bool           `json:"deleted,omitempty"`
	SecretKind     SecretKind     `json:"secret_kind,omitempty"`
	PayloadHash    string         `json:"payload_hash,omitempty"`
	FileMode       uint32         `json:"file_mode,omitempty"`
	Size           int64          `json:"size,omitempty"`
	LastLocalHash  string         `json:"last_local_hash"`          // 上次同步时的本地内容哈希
	LastRemoteHash string         `json:"last_remote_hash"`         // 上次同步时的云端内容哈希
	Selected       bool           `json:"selected"`                 // 用户历史勾选偏好
	Strategy       Strategy       `json:"strategy"`                 // 用户选用的合并策略
	UpdatedAt      time.Time      `json:"updated_at"`               // 记录更新时间
	LastSyncedAt   time.Time      `json:"last_synced_at,omitempty"` // 该项最近确认同步时间
}

// LocalState 本地状态配置文件结构体（按 GitHub vault 隔离）
type LocalState struct {
	Version          string                      `json:"version"`                      // 状态文件版本
	VaultID          string                      `json:"vault_id,omitempty"`           // owner/repository，防止不同 vault 共用错误基线
	LastSyncedAt     time.Time                   `json:"last_synced_at"`               // 上次全量同步时间
	RemoteSnapshotID string                      `json:"remote_snapshot_id"`           // 最近同步的远端快照 ID
	UploadSnapshotID string                      `json:"upload_snapshot_id,omitempty"` // 本机在仓库中复用的稳定目录
	DeviceID         string                      `json:"device_id,omitempty"`          // 本机随机 ID，不依赖可变 hostname
	RecipientsHash   string                      `json:"recipients_hash,omitempty"`    // 最近上传所用接收者表哈希
	LastUploadedAt   time.Time                   `json:"last_uploaded_at,omitempty"`
	LastDownloadedAt time.Time                   `json:"last_downloaded_at,omitempty"`
	TrackedItems     map[string]TrackedItemState `json:"tracked_items"` // key: item.ID
}

// SnapshotMetadata 是可公开的快照索引元数据。它不包含本地路径或配置内容。
// 真正的快照完整性仍由加密 Manifest 中的字段校验。
type SnapshotMetadata struct {
	Version    string    `json:"version"`
	SnapshotID string    `json:"snapshot_id"`
	Hostname   string    `json:"hostname"`
	CreatedAt  time.Time `json:"created_at"`
	DeviceID   string    `json:"device_id,omitempty"`
}
