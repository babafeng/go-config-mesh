package scanner

import (
	"path/filepath"
	"strings"
)

// DefaultIgnoreDirs 需要在配置同步中严格排除的 AI 对话历史、向量索引、模型二进制、扩展及 Electron 缓存
var DefaultIgnoreDirs = []string{
	// ==================== 1. AI 专用大体积缓存 & 会话历史 ====================
	"brain",                       // Gemini / Antigravity 对话快照与系统 trajectory
	".system_generated",           // 系统自动生成的 transcripts 与 logs
	"sessions",                    // Claude Code / Codex / Continue 会话记录
	"archived_sessions",           // 归档的历史会话
	"transcripts",                 // 逐字稿与对话详情
	"index",                       // Continue.dev / 向量检索库 (LanceDB)
	"models",                      // Ollama / 本地 LLM 模型权重二进制 (数 GB ~ 数十 GB)
	"vector_cache",                // 向量特征缓存
	"embeddings",                  // 嵌入向量数据库
	"code_tracker",                // 代码追踪与索引快照
	"antigravity-browser-profile", // 自动化浏览器 Profile 缓存
	"antigravity-backup",          // 自动备份历史
	"computer-use",                // 计算机控制与截图录屏缓存
	"attachments",                 // 历史对话附件
	"state",                       // 运行时状态持久化
	"sqlite",                      // 数据库目录
	"ambient-suggestions",         // 辅助建议缓存
	"bin",                         // 内置二进制工具 (如 webm_encoder)

	// ==================== 2. IDE / VS Code / Electron 运行期缓存 ====================
	"extensions",       // 编辑器插件解压运行目录 (可重装，无需同步数 GB 二进制)
	"History",          // 本地文件编辑时间轴快照
	"workspaceStorage", // 工作区临时会话状态
	"globalStorage",    // 全局插件状态数据
	"Code Cache",       // V8 编译字节码缓存
	"GPUCache",         // GPU 渲染着色器缓存
	"VideoDecodeStats", // 视频解码指标
	"blob_storage",     // Electron Blob 数据存储
	"Service Worker",   // Service Worker 缓存

	// ==================== 3. 编程语言依赖与构建产物 ====================
	"node_modules",            // Node 依赖
	"__pycache__",             // Python 字节码
	".pytest_cache",           // Pytest 运行缓存
	".mypy_cache",             // Mypy 类型缓存
	".ruff_cache",             // Ruff linter 缓存
	"dist", "build", "target", // 编译产物
	"cache", ".cache", "CachedData", // 通用缓存

	// ==================== 4. 系统运行日志与临时目录 ====================
	"logs", "log", // 日志目录
	"tmp", "temp", // 临时目录
	"history", // 历史记录
}

// DefaultIgnoreFiles 忽略的文件模式（排除日志、会话 JSONL、向量数据库、临时文件与大二进制）
var DefaultIgnoreFiles = []string{
	// 运行日志与对话快照
	"*.log", "*.log.*", "*.transcript.jsonl", "*.transcripts.jsonl", "*.jsonl",
	// 向量库与 SQLite 临时数据库
	"*.sqlite", "*.sqlite3", "*.sqlite-wal", "*.sqlite-shm", "*.db-shm", "*.db-wal",
	"*.db",
	// Aider & 命令行输入历史文件
	"*.chat.history.md", "*.input.history", "*.tags.cache*",
	// 临时与 Socket 文件
	"*.tmp", "*.temp", "*.sock", "agent.*",
	".DS_Store", "*.pyc", "*.pyo", "*.swp", "*.orig", "*.bak",
	// 媒体录屏与大二进制文件
	"*.webm", "*.mp4", "*.bin",
}

// ShouldIgnorePath 检查目录内相对路径是否应该被排除
func ShouldIgnorePath(relPath string) bool {
	cleanRel := filepath.ToSlash(filepath.Clean(relPath))
	parts := strings.Split(cleanRel, "/")

	for _, part := range parts {
		// 检查前缀通配 (如 backup-*)
		if strings.HasPrefix(strings.ToLower(part), "backup-") {
			return true
		}

		for _, ignoreDir := range DefaultIgnoreDirs {
			if strings.EqualFold(part, ignoreDir) {
				return true
			}
		}
	}

	baseName := filepath.Base(cleanRel)
	for _, pattern := range DefaultIgnoreFiles {
		if matched, _ := filepath.Match(pattern, baseName); matched {
			return true
		}
	}

	return false
}
