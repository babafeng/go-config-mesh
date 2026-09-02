package github

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v60/github"
	"golang.org/x/oauth2"
)

const (
	DefaultRepoName = "config-mesh-vault"
	DefaultRepoDesc = "Private configuration mesh managed by config-mesh (Age Encrypted)"
)

// RepoClient 封装 GitHub 仓库操作
type RepoClient struct {
	client *github.Client
	ctx    context.Context
}

// NewRepoClient 创建 GitHub API 客户端
func NewRepoClient(token string) *RepoClient {
	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	)
	tc := oauth2.NewClient(ctx, ts)
	tc.Timeout = 30 * time.Second
	return &RepoClient{
		client: github.NewClient(tc),
		ctx:    ctx,
	}
}

// EnsurePrivateRepo 检查指定私有仓库是否存在，若不存在则自动创建私有仓库
func (rc *RepoClient) EnsurePrivateRepo(owner, repoName string) (*github.Repository, error) {
	if repoName == "" {
		repoName = DefaultRepoName
	}

	// 1. 尝试获取仓库信息
	repo, resp, err := rc.client.Repositories.Get(rc.ctx, owner, repoName)
	if err == nil && repo != nil {
		if !repo.GetPrivate() {
			return nil, fmt.Errorf("拒绝使用公开仓库 %s/%s：config-mesh vault 必须是私有仓库", owner, repoName)
		}
		if !strings.EqualFold(repo.GetOwner().GetLogin(), owner) {
			return nil, fmt.Errorf("仓库 owner 不匹配：实际 %s，期望 %s", repo.GetOwner().GetLogin(), owner)
		}
		return repo, nil
	}

	// 若非 404 错误（如权限或网络错误），直接返回
	if resp != nil && resp.StatusCode != http.StatusNotFound {
		return nil, fmt.Errorf("查询 GitHub 仓库失败: %w", err)
	}

	// 2. 仓库不存在，创建私有仓库
	isPrivate := true
	autoInit := true
	desc := DefaultRepoDesc

	newRepo := &github.Repository{
		Name:        &repoName,
		Description: &desc,
		Private:     &isPrivate,
		AutoInit:    &autoInit, // 自动生成初始 README，保证分支存在
	}

	authUser, err := rc.GetAuthenticatedUser()
	if err != nil {
		return nil, err
	}
	createOwner := ""
	if !strings.EqualFold(owner, authUser) {
		createOwner = owner // go-github 对非空值使用组织建仓 API
	}
	createdRepo, _, err := rc.client.Repositories.Create(rc.ctx, createOwner, newRepo)
	if err != nil {
		return nil, fmt.Errorf("自动创建 GitHub 私有仓库失败: %w", err)
	}
	if !createdRepo.GetPrivate() || !strings.EqualFold(createdRepo.GetOwner().GetLogin(), owner) {
		return nil, fmt.Errorf("创建后的仓库身份校验失败: %s/%s", createdRepo.GetOwner().GetLogin(), createdRepo.GetName())
	}

	return createdRepo, nil
}

// GetAuthenticatedUser 获取当前 Token 对应的 GitHub 用户名
func (rc *RepoClient) GetAuthenticatedUser() (string, error) {
	user, _, err := rc.client.Users.Get(rc.ctx, "")
	if err != nil {
		return "", fmt.Errorf("获取 GitHub 用户信息失败: %w", err)
	}
	return user.GetLogin(), nil
}
