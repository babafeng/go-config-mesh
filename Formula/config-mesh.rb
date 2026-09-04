class ConfigMesh < Formula
  desc "安全、轻量的 macOS 开发环境配置端到端加密同步工具"
  homepage "https://github.com/babafeng/go-config-mesh"
  license "Unlicense"
  head "https://github.com/babafeng/go-config-mesh.git", branch: "main"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w -X main.Version=#{version}"), "./cmd/config-mesh"
  end

  test do
    assert_match "config-mesh", shell_output("#{bin}/config-mesh version")
  end
end
