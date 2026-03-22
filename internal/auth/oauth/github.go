package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/endpoints"
)

// GitHubProvider implements Provider for GitHub OAuth.
type GitHubProvider struct {
	cfg *oauth2.Config
}

// NewGitHubProvider creates a GitHub OAuth provider.
func NewGitHubProvider(clientID, clientSecret, callbackURL string) *GitHubProvider {
	return &GitHubProvider{
		cfg: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     endpoints.GitHub,
			Scopes:       []string{"user:email"},
			RedirectURL:  callbackURL,
		},
	}
}

func (p *GitHubProvider) Name() string { return "github" }

func (p *GitHubProvider) AuthCodeURL(state string) string {
	return p.cfg.AuthCodeURL(state, oauth2.AccessTypeOnline)
}

func (p *GitHubProvider) Exchange(ctx context.Context, code string) (UserProfile, error) {
	token, err := p.cfg.Exchange(ctx, code)
	if err != nil {
		return UserProfile{}, fmt.Errorf("exchange code: %w", err)
	}

	client := p.cfg.Client(ctx, token)

	profile, err := fetchGitHubUser(client)
	if err != nil {
		return UserProfile{}, err
	}

	// GitHub may not include email if the user's email is private.
	if profile.Email == "" {
		email, err := fetchGitHubPrimaryEmail(client)
		if err != nil {
			return UserProfile{}, err
		}
		profile.Email = email
	}

	return profile, nil
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func fetchGitHubUser(client *http.Client) (UserProfile, error) {
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		return UserProfile{}, fmt.Errorf("fetch github user: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return UserProfile{}, fmt.Errorf("github /user returned %d", resp.StatusCode)
	}

	var u githubUser
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return UserProfile{}, fmt.Errorf("decode github user: %w", err)
	}

	name := u.Name
	if name == "" {
		name = u.Login
	}

	return UserProfile{
		ProviderID: strconv.FormatInt(u.ID, 10),
		Email:      u.Email,
		Name:       name,
	}, nil
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

func fetchGitHubPrimaryEmail(client *http.Client) (string, error) {
	resp, err := client.Get("https://api.github.com/user/emails")
	if err != nil {
		return "", fmt.Errorf("fetch github emails: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github /user/emails returned %d", resp.StatusCode)
	}

	var emails []githubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", fmt.Errorf("decode github emails: %w", err)
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}

	return "", fmt.Errorf("github account has no verified primary email")
}
