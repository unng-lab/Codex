package proxy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"brainhub/internal/authstore"
)

type AuthTokens struct {
	AccessToken string
	AccountID   string
}

func LoadAuthTokens(path string) (AuthTokens, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return AuthTokens{}, fmt.Errorf("auth file path is required")
	}

	b, err := os.ReadFile(p)
	if err != nil {
		return AuthTokens{}, fmt.Errorf("read auth file: %w", err)
	}

	var doc struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return AuthTokens{}, fmt.Errorf("parse auth file: %w", err)
	}

	out := AuthTokens{
		AccessToken: strings.TrimSpace(doc.Tokens.AccessToken),
		AccountID:   strings.TrimSpace(doc.Tokens.AccountID),
	}
	if out.AccessToken == "" {
		return AuthTokens{}, fmt.Errorf("auth file is missing tokens.access_token")
	}
	if out.AccountID == "" {
		return AuthTokens{}, fmt.Errorf("auth file is missing tokens.account_id")
	}
	return out, nil
}

func DefaultAuthFilePath() string {
	return authstore.DefaultAuthFilePath()
}
