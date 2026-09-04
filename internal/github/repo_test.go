package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	gh "github.com/google/go-github/v60/github"
)

type mockTransport struct {
	handler http.Handler
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	m.handler.ServeHTTP(rec, req)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testRepoClient(t *testing.T, handler http.HandlerFunc) *RepoClient {
	t.Helper()
	transport := &mockTransport{handler: handler}
	client := gh.NewClient(&http.Client{Transport: transport})
	baseURL, err := url.Parse("https://api.github.local/")
	if err != nil {
		t.Fatal(err)
	}
	client.BaseURL = baseURL
	client.UploadURL = baseURL
	return &RepoClient{client: client, ctx: context.Background()}
}

func TestEnsurePrivateRepoRejectsPublicRepo(t *testing.T) {
	client := testRepoClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/alice/vault" {
			t.Fatalf("意外请求路径: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "vault", "private": false, "owner": map[string]any{"login": "alice"},
		})
	})
	if _, err := client.EnsurePrivateRepo("alice", "vault"); err == nil || !strings.Contains(err.Error(), "公开仓库") {
		t.Fatalf("必须拒绝公开 vault，实际: %v", err)
	}
}

func TestEnsurePrivateRepoCreatesOrganizationRepo(t *testing.T) {
	client := testRepoClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/vault":
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "alice"})
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/acme/repos":
			var body struct {
				Name     string `json:"name"`
				Private  bool   `json:"private"`
				AutoInit bool   `json:"auto_init"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Name != "vault" || !body.Private || !body.AutoInit {
				t.Fatalf("建仓参数不安全: %+v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "vault", "private": true, "owner": map[string]any{"login": "acme"},
			})
		default:
			t.Fatalf("意外请求: %s %s", r.Method, r.URL.Path)
		}
	})
	repo, err := client.EnsurePrivateRepo("acme", "vault")
	if err != nil {
		t.Fatal(err)
	}
	if !repo.GetPrivate() || repo.GetOwner().GetLogin() != "acme" {
		t.Fatalf("组织私有仓库校验失败: %+v", repo)
	}
}

func TestGetTokenPrefersEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "  github-token  ")
	t.Setenv("GH_TOKEN", "other-token")
	token, err := NewAuthManager().GetToken("")
	if err != nil {
		t.Fatal(err)
	}
	if token != "github-token" {
		t.Fatalf("环境 Token 优先级或清理错误: %q", token)
	}
}

func TestEnsurePrivateRepoNetworkError(t *testing.T) {
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused (network down)")
	})
	client := gh.NewClient(&http.Client{Transport: transport})
	baseURL, _ := url.Parse("https://api.github.local/")
	client.BaseURL = baseURL
	repoClient := &RepoClient{client: client, ctx: context.Background()}

	_, err := repoClient.EnsurePrivateRepo("alice", "vault")
	if err == nil || !strings.Contains(err.Error(), "查询 GitHub 仓库失败") {
		t.Fatalf("网络异常时必须直接报错，实际: %v", err)
	}
}

