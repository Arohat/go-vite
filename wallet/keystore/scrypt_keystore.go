package keystore

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/pborman/uuid"
	"github.com/vitelabs/go-vite/common/types"
	vcrypto "github.com/vitelabs/go-vite/crypto"
	"github.com/vitelabs/go-vite/crypto/ed25519"
	"golang.org/x/crypto/scrypt"
	"io/ioutil"
	"os"
	"path/filepath"
	"time"
)

const (
	// StandardScryptN is the N parameter of Scrypt encryption algorithm, using 256MB
	// memory and taking approximately 1s CPU time on a modern processor.
	StandardScryptN = 1 << 18

	// StandardScryptP is the P parameter of Scrypt encryption algorithm, using 256MB
	// memory and taking approximately 1s CPU time on a modern processor.
	StandardScryptP = 1

	scryptR      = 8
	scryptKeyLen = 32

	aesMode    = "aes-256-gcm"
	scryptName = "scrypt"
)

type keyStorePassphrase struct {
	keysDirPath string
}

func (ks keyStorePassphrase) ExtractKey(addr types.Address, password string) (*Key, error) {
	keyjson, err := ioutil.ReadFile(fullKeyFileName(ks.keysDirPath, addr))
	if err != nil {
		return nil, err
	}

	key, err := DecryptKey(keyjson, password)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func (ks keyStorePassphrase) StoreKey(key *Key, password string) error {
	keyjson, err := EncryptKey(key, password)
	if err != nil {
		return err
	}
	return writeKeyFile(fullKeyFileName(ks.keysDirPath, key.Address), keyjson)
}

func parseJson(keyjson []byte) (k *encryptedKeyJSON, kAddress *types.Address, cipherPriv, nonce, salt []byte, err error) {
	k = new(encryptedKeyJSON)
	// parse and check encryptedKeyJSON params
	if err := json.Unmarshal(keyjson, k); err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if k.Version != keystoreVersion {
		return nil, nil, nil, nil, nil, fmt.Errorf("version number error : %v", k.Version)
	}
	kid := uuid.Parse(k.Id)
	if kid == nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("uuid  error : %v", kid)
	}

	if !types.IsValidHexAddress(k.HexAddress) {
		return nil, nil, nil, nil, nil, fmt.Errorf("address invalid ： %v", k.HexAddress)
	}
	addr, err := types.HexToAddress(k.HexAddress)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// parse and check  cryptoJSON params
	if k.Crypto.CipherName != aesMode {
		return nil, nil, nil, nil, nil, fmt.Errorf("cipherName  error : %v", k.Crypto.CipherName)
	}
	if k.Crypto.KDF != scryptName {
		return nil, nil, nil, nil, nil, fmt.Errorf("scryptName  error : %v", k.Crypto.KDF)
	}
	cipherPriv, err = hex.DecodeString(k.Crypto.CipherText)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	nonce, err = hex.DecodeString(k.Crypto.Nonce)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	// parse and check  scryptParams params
	scryptParams := k.Crypto.ScryptParams
	salt, err = hex.DecodeString(scryptParams.Salt)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}

	return k, &addr, cipherPriv, nonce, salt, nil
}

func DecryptKey(keyjson []byte, password string) (*Key, error) {
	k, kAddress, cipherPriv, nonce, salt, err := parseJson(keyjson)
	if err != nil {
		return nil, err
	}
	scryptParams := k.Crypto.ScryptParams
	kid := uuid.Parse(k.Id)

	// begin decrypt
	derivedKey, err := scrypt.Key([]byte(password), salt, scryptParams.N, scryptParams.R, scryptParams.P, scryptParams.KeyLen)
	if err != nil {
		return nil, err
	}

	var pribyte = make([]byte, ed25519.PrivateKeySize)
	pribyte, err = vcrypto.AesGCMDecrypt(derivedKey[:32], cipherPriv, []byte(nonce))
	if err != nil {
		return nil, err
	}

	privKey := ed25519.PrivateKey(pribyte)
	generateAddr := types.PrikeyToAddress(privKey)
	if !bytes.Equal(generateAddr[:], kAddress[:]) {
		return nil,
			fmt.Errorf("address content not equal. In file it is : %s  but generated is : %s",
				k.HexAddress, generateAddr.Hex())
	}

	return &Key{
		Id:         kid,
		Address:    generateAddr,
		PrivateKey: &privKey,
	}, nil
}

func EncryptKey(key *Key, password string) ([]byte, error) {
	n := StandardScryptN
	p := StandardScryptP
	pwdArray := []byte(password)
	salt := vcrypto.GetEntropyCSPRNG(32)
	derivedKey, err := scrypt.Key(pwdArray, salt, n, scryptR, p, scryptKeyLen)
	if err != nil {
		return nil, err
	}
	encryptKey := derivedKey[:32]

	ciphertext, nonce, err := vcrypto.AesGCMEncrypt(encryptKey, *key.PrivateKey)
	if err != nil {
		return nil, err
	}

	ScryptParams := scryptParams{
		N:      n,
		R:      scryptR,
		P:      p,
		KeyLen: scryptKeyLen,
		Salt:   hex.EncodeToString(salt),
	}

	cryptoJSON := cryptoJSON{
		CipherName:   aesMode,
		CipherText:   hex.EncodeToString(ciphertext),
		Nonce:        hex.EncodeToString(nonce),
		KDF:          scryptName,
		ScryptParams: ScryptParams,
	}

	encryptedKeyJSON := encryptedKeyJSON{

		HexAddress: key.Address.Hex(),
		Crypto:     cryptoJSON,
		Id:         key.Id.String(),
		Version:    keystoreVersion,
		Timestamp:  time.Now().UTC().Unix(),
	}

	return json.Marshal(encryptedKeyJSON)
}

func writeKeyFile(file string, content []byte) error {

	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}

	f, err := ioutil.TempFile(filepath.Dir(file), "."+filepath.Base(file)+".tmp")
	if err != nil {
		return err
	}

	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	f.Close()
	return os.Rename(f.Name(), file)
}
