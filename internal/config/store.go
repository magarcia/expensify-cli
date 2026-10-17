package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/magarcia/expensify-cli/internal/fault"
)

type Credentials struct {
	AuthToken         string `json:"authToken"`
	PartnerUserID     string `json:"partnerUserID"`
	PartnerUserSecret string `json:"partnerUserSecret"`
	Email             string `json:"email,omitempty"`
	AccountID         int64  `json:"accountID,omitempty"`
}

type Store struct {
	dir     string
	initErr error
	keyring credentialKeyring
	mode    string
	storage atomic.Value
}

func New(dir string) *Store {
	if dir == "" {
		dir = os.Getenv("EXPENSIFY_CONFIG_DIR")
	}
	if dir == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if !filepath.IsAbs(base) {
			home, err := os.UserHomeDir()
			if err != nil {
				return &Store{initErr: fault.New("CONFIG-001", "Cannot locate your configuration directory.", "Set EXPENSIFY_CONFIG_DIR to a private directory.", 1)}
			}
			base = filepath.Join(home, ".config")
		}
		dir = filepath.Join(base, "expensify")
	}
	return &Store{dir: filepath.Clean(dir), keyring: systemKeyring{}, mode: os.Getenv("EXPENSIFY_CREDENTIAL_STORE")}
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Path() string { return filepath.Join(s.dir, "credentials.json") }

// Resolve keeps environment credentials isolated from a saved account's refresh credentials.
func Resolve(stored Credentials) (Credentials, string, error) {
	token := os.Getenv("EXPENSIFY_AUTH_TOKEN")
	userID := os.Getenv("EXPENSIFY_PARTNER_USER_ID")
	secret := os.Getenv("EXPENSIFY_PARTNER_USER_SECRET")
	if (userID == "") != (secret == "") {
		return Credentials{}, "none", fault.New("AUTH-002", "Environment device credentials are incomplete.", "Set EXPENSIFY_PARTNER_USER_ID and EXPENSIFY_PARTNER_USER_SECRET together.", 3)
	}
	if token != "" || userID != "" {
		return Credentials{AuthToken: token, PartnerUserID: userID, PartnerUserSecret: secret}, "environment", nil
	}
	if stored.AuthToken != "" || stored.PartnerUserID != "" {
		return stored, "stored", nil
	}
	return Credentials{}, "none", nil
}

func (s *Store) checkDir(create bool) error {
	if s.initErr != nil {
		return s.initErr
	}
	info, err := os.Lstat(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil
		}
		if err := createPrivateDir(s.dir); err != nil {
			return fileError("create", "configuration directory")
		}
		info, err = os.Lstat(s.dir)
	}
	if err != nil {
		return fileError("inspect", "configuration directory")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fault.New("CONFIG-002", "The configuration directory must be a real directory.", "Choose a private directory with --config-dir.", 1)
	}
	return checkPrivatePermissions(s.dir, info)
}

func privateFileInfo(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fileError("inspect", filepath.Base(path))
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fault.New("CONFIG-002", "Configuration files must be regular files, without symbolic links.", "Replace the unsafe file before continuing.", 1)
	}
	if err := checkPrivatePermissions(path, info); err != nil {
		return nil, err
	}
	return info, nil
}

func (s *Store) read(name string, value any) error {
	if err := s.checkDir(false); err != nil {
		return err
	}
	path := filepath.Join(s.dir, name)
	info, err := privateFileInfo(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return fileError("read", name)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return fault.New("CONFIG-002", "A configuration file changed while it was being opened.", "Retry after ensuring the configuration directory is private.", 1)
	}
	return decode(file, value, name)
}

func decode(reader io.Reader, value any, name string) error {
	const maxSize = 1 << 20
	data, err := io.ReadAll(io.LimitReader(reader, maxSize+1))
	if err != nil || len(data) > maxSize {
		return fileError("parse", name)
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return fileError("parse", name)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fileError("parse", name)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return fileError("parse", name)
	}
	return nil
}

func (s *Store) write(name string, value any) error {
	if err := s.checkDir(true); err != nil {
		return err
	}
	path := filepath.Join(s.dir, name)
	if _, err := privateFileInfo(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(s.dir, "."+name+"-*")
	if err != nil {
		return fileError("write", name)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := makeFilePrivate(file); err != nil {
		return fileError("secure", name)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fileError("write", name)
	}
	if err := file.Sync(); err != nil {
		return fileError("write", name)
	}
	if err := file.Close(); err != nil {
		return fileError("write", name)
	}
	if _, err := privateFileInfo(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := replacePrivateFile(file.Name(), path); err != nil {
		return fileError("write", name)
	}
	return nil
}

func fileError(operation, name string) error {
	return fault.New("CONFIG-001", "Cannot "+operation+" "+name+".", "Check the configuration path, permissions, and JSON format.", 1)
}
