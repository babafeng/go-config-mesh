# config-mesh 🕸️

> 专为 macOS 打造的安全、现代、自动化的端到端加密配置同步工具。

基于 **GitHub 私有仓库** + **本地 SSH 密钥 (Age) 端到端加密** + **现代化 TUI 交互**。

> 信任边界：age 提供内容机密性和密文完整性，但当前快照没有独立的发送设备签名。拥有仓库写权限者仍可用公开接收者密钥伪造一个可解密的新快照；下载前必须核对快照来源。首次设备信任与签名链不在 v1.0.0 的能力范围内。
>
> 本地回滚快照以明文保存在 `~/.config-mesh/backups/`，目录权限会强制为 `0700`；云端负载和 Manifest 才使用 age 加密。为阻止软链接逃逸，预设配置根本身是软链接时不会进入扫描结果。

---

## ✨ 核心特性

- 🔒 **端到端内容加密**：配置和 Manifest 在本地使用 SSH 多接收者 age 加密；GitHub 只能看到密文、接收者公钥和快照时间/主机名元数据。
- 📦 **每台设备一个稳定仓库目录**：首次上传生成 `hosts/<hostname>-<device-id>/`；之后原位更新同一目录，版本历史交给 Git commit 保存，不再每次上传创建新目录。
- 🔐 **受控凭据同步**：自动发现 SSH 私钥/公钥、`~/.aws/credentials` 和 `~/.aliyun/config.json`。凭据默认不勾选，必须显式选择并输入 `SYNC-SECRETS`，只进入 age 加密负载，下载后权限强制为 `0600`。
- 🔍 **macOS 智能扫描**：自动探测包括 `~/.zshrc` 在内的常见配置。已知配置中检测到 Token/密码时不再静默消失，而是标为受控敏感配置，进入二次确认和 age 加密流程；未列入范围的 kubeconfig、`.env*`、GPG/证书私钥仍会被拒绝。
- 🖥️ **现代化 TUI 交互**：分类标题提供总复选框及未选/半选/全选状态；支持滚动视口、PgUp/PgDn、Home/End 和单项选择。
- 🧠 **按 vault 隔离的本地状态跟踪与偏好记忆 (`~/.config-mesh/states/`)**：
  - JSON 哈希表记录每项路径、选择状态、本地/远端哈希、负载哈希和最近同步时间；
  - 记录设备 ID、稳定上传目录、接收者表哈希及最近上传/下载时间；
  - 内容及接收者未变化时复用原密文，不生成目录或空提交；接收者变化时自动重新加密。
- 🏷️ **快照归属识别**：远端快照列表按本机 `upload_snapshot_id` / `device_id` 标记为 `[本机]`、`[其他设备]` 或 `[设备未知]`；有其他设备快照时下载默认优先选择其他设备。
- 🛡️ **精准前置快照备份与智能合并**：
  - **下载前完整备份**：同时记录“原本不存在”的配置，使回滚可删除本次新建项。
  - 安全默认为 **覆盖**；可选 Shell/Git 标记块、JSON 深度合并和目录 overlay。
  - 支持一键 `config-mesh rollback` 安全回滚。

---

## 📖 设计与架构文档

详见系统完整设计方案：[DESIGN.md](DESIGN.md)。

---

## 🚀 快速上手

要求 Go `1.25.13` 或更高补丁版本；较早的 Go 1.25 工具链包含本项目可触达的标准库漏洞。

```bash
# 1. 首次运行：一键认证、建仓、扫描、加密并同步
config-mesh apply <your-github-username>

# 2. 查看当前配置网格状态与哈希差异
config-mesh status

# 3. 在已授权的设备 A 上添加设备 B 的公钥
config-mesh key add /path/to/device-b-id_ed25519.pub <your-github-username>

# 4. 在 A 上更新本机稳定目录；接收者变化会触发重新加密
config-mesh apply <your-github-username>

# 5. 在设备 B 上选择并解密 A 的稳定配置槽位
config-mesh apply <your-github-username>
```

同步凭据时，在 TUI 中手动勾选带有 `[🔐凭据·默认不选]` 标记的项目，并按提示输入：

```text
SYNC-SECRETS
```

当前受控范围：

- SSH：`~/.ssh/` 下可解析的私钥及对应 `.pub` 文件；不包含 `authorized_keys`、`known_hosts`。
- AWS：`~/.aws/credentials`（官方默认共享凭据文件）。
- Alibaba Cloud CLI：`~/.aliyun/config.json`（官方 macOS/Linux 配置及凭据文件）。
- 已知单文件配置：例如 `~/.zshrc` 检测到 Token/密码时，以 `detected_config_secret` 类型同步并强制权限为 `0600`。
