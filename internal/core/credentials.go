package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Sync tokens live in the user's config directory, not in .spor/. The store may
// sit inside a Dropbox or Drive folder (docs/design-spec.md §3 warns against it
// but does not prevent it), and a project directory is the wrong place for a
// secret. The remote URL and project id are not secret and stay in the store, so
// they travel with the project.

// TokenEnvVar overrides the stored token, matching the $SPOR_PAGER convention
// and giving CI somewhere to put a token without writing a file.
const TokenEnvVar = "SPOR_TOKEN"

// credentialsFile is the on-disk shape: remote origin -> credential.
type credentialsFile map[string]credential

type credential struct {
	Token string `json:"token"`
}

// credentialsPath returns the path of the credentials file.
func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating config directory: %w", err)
	}
	return filepath.Join(dir, "spor", "credentials.json"), nil
}

// credentialKey reduces a remote URL to the origin the token belongs to, so one
// token covers every project on a server and a trailing slash cannot produce a
// second entry.
func credentialKey(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", fmt.Errorf("parsing remote url %q: %w", rawURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("remote url %q is missing a scheme or host", rawURL)
	}
	return u.Scheme + "://" + u.Host, nil
}

// readCredentials loads the credentials file, treating absence as empty.
func readCredentials() (credentialsFile, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return credentialsFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	creds := credentialsFile{}
	if err := json.Unmarshal(b, &creds); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return creds, nil
}

// loadToken returns the token for a remote. $SPOR_TOKEN wins when set, so a
// throwaway or CI token needs no file. An empty result is not an error: a server
// may not require auth.
func loadToken(rawURL string) (string, error) {
	if t := strings.TrimSpace(os.Getenv(TokenEnvVar)); t != "" {
		return t, nil
	}
	key, err := credentialKey(rawURL)
	if err != nil {
		return "", err
	}
	creds, err := readCredentials()
	if err != nil {
		return "", err
	}
	return creds[key].Token, nil
}

// saveToken records a token for a remote, replacing any previous one. The file
// is written 0600 through a temp file and a rename, so an interrupted write
// cannot leave a truncated credentials file behind.
func saveToken(rawURL, token string) error {
	key, err := credentialKey(rawURL)
	if err != nil {
		return err
	}
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	creds, err := readCredentials()
	if err != nil {
		return err
	}
	if token == "" {
		delete(creds, key)
	} else {
		creds[key] = credential{Token: token}
	}

	b, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), "credentials-*.json")
	if err != nil {
		return fmt.Errorf("creating temp credentials file: %w", err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("securing credentials file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("writing credentials: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("installing credentials file: %w", err)
	}
	return nil
}
