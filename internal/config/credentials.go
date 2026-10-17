package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/magarcia/expensify-cli/internal/fault"
	"github.com/zalando/go-keyring"
)

const keyringService = "expensify-cli"

type credentialKeyring interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type systemKeyring struct{}

func (systemKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (systemKeyring) Set(service, user, password string) error {
	// Stay within Windows' secret limit and macOS' encoded command limit before
	// starting a native write, which can leave a child waiting on oversized input.
	if len(password) > 2560 {
		return keyring.ErrSetDataTooBig
	}
	return keyring.Set(service, user, password)
}
func (systemKeyring) Delete(service, user string) error { return keyring.Delete(service, user) }

// Storage reports the backend after the last successful credential operation.
func (s *Store) Storage() string {
	if value := s.storage.Load(); value != nil {
		return value.(string)
	}
	return "none"
}

// CredentialStore returns the configured storage preference without accessing secrets.
func (s *Store) CredentialStore() (string, error) {
	switch s.mode {
	case "", "auto":
		return "auto", nil
	case "keyring", "file":
		return s.mode, nil
	default:
		return "", fault.New("CONFIG-003", "The credential store selection is invalid.", "Set EXPENSIFY_CREDENTIAL_STORE to auto, keyring, or file.", 1)
	}
}

func (s *Store) Load() (Credentials, error) {
	user, exists, err := s.credentialLocation()
	if err != nil {
		return Credentials{}, err
	}
	if exists {
		var credentials Credentials
		if err := s.read("credentials.json", &credentials); err != nil {
			return Credentials{}, err
		}
		if err := checkCredentialPair(credentials); err != nil {
			return Credentials{}, err
		}
		// A fallback file is authoritative until migration commits, including when
		// the keyring still holds an older account from before an unavailable write.
		migrated, err := s.saveKeyring(user, credentials)
		if err != nil {
			return Credentials{}, err
		}
		if !migrated {
			s.storage.Store("file")
		}
		return credentials, nil
	}
	if s.mode == "file" {
		s.storage.Store("none")
		return Credentials{}, nil
	}
	data, err := s.keyring.Get(keyringService, user)
	if errors.Is(err, keyring.ErrNotFound) {
		s.storage.Store("none")
		return Credentials{}, nil
	}
	if err != nil {
		return Credentials{}, keyringError("read")
	}
	var credentials Credentials
	if err := decode(strings.NewReader(data), &credentials, "keyring credentials"); err != nil {
		return Credentials{}, fault.New("AUTH-011", "Saved keyring credentials are not valid JSON.", "Run expensify auth login to replace them, or auth logout to remove them.", 3)
	}
	if err := checkCredentialPair(credentials); err != nil {
		return Credentials{}, err
	}
	s.storage.Store("keyring")
	return credentials, nil
}

func (s *Store) Save(credentials Credentials) error {
	if err := checkCredentialPair(credentials); err != nil {
		return err
	}
	user, _, err := s.credentialLocation()
	if err != nil {
		return err
	}
	return s.saveCredentials(user, credentials)
}

func (s *Store) saveCredentials(user string, credentials Credentials) error {
	if saved, err := s.saveKeyring(user, credentials); saved || err != nil {
		return err
	}
	if err := s.write("credentials.json", credentials); err != nil {
		return err
	}
	s.storage.Store("file")
	return nil
}

func (s *Store) saveKeyring(user string, credentials Credentials) (bool, error) {
	if s.mode == "file" {
		return false, nil
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return false, fileError("encode", "credentials")
	}
	if err := s.keyring.Set(keyringService, user, string(data)); err != nil {
		if s.mode == "keyring" {
			if errors.Is(err, keyring.ErrSetDataTooBig) {
				return false, fault.New("AUTH-011", "The credentials exceed the OS keyring's size limit.", "Check the imported credentials, or select EXPENSIFY_CREDENTIAL_STORE=file to use private file storage.", 3)
			}
			return false, keyringError("save")
		}
		return false, nil
	}
	if err := s.removeCredentialFile(); err != nil {
		return false, err
	}
	s.storage.Store("keyring")
	return true, nil
}

func (s *Store) Logout() error {
	user, _, err := s.credentialLocation()
	if err != nil {
		return err
	}
	var keyringErr error
	if s.mode != "file" {
		if err := s.keyring.Delete(keyringService, user); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			keyringErr = keyringError("remove")
		}
	}
	if err := s.removeCredentialFile(); err != nil {
		return err
	}
	if keyringErr != nil {
		return keyringErr
	}
	s.storage.Store("none")
	return nil
}

func (s *Store) credentialLocation() (user string, exists bool, err error) {
	if _, err := s.CredentialStore(); err != nil {
		return "", false, err
	}
	if err := s.checkDir(false); err != nil {
		return "", false, err
	}
	_, err = privateFileInfo(s.Path())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	exists = err == nil
	abs, err := filepath.Abs(s.dir)
	if err != nil {
		return "", false, fileError("resolve", "configuration directory")
	}
	digest := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(digest[:]), exists, nil
}

func (s *Store) removeCredentialFile() error {
	if _, err := privateFileInfo(s.Path()); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.Remove(s.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fileError("remove", "credentials.json")
	}
	return nil
}

func checkCredentialPair(credentials Credentials) error {
	if (credentials.PartnerUserID == "") != (credentials.PartnerUserSecret == "") {
		return fault.New("AUTH-002", "Device credentials require both a user ID and a secret.", "Run expensify auth login to replace incomplete credentials.", 3)
	}
	return nil
}

func keyringError(operation string) error {
	return fault.New("AUTH-011", "Cannot "+operation+" credentials in the OS keyring.", "Unlock or enable your OS keyring and retry. EXPENSIFY_CREDENTIAL_STORE=file selects private file storage; logout in file mode removes only that file.", 3)
}
