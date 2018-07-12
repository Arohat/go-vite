package keystore

import (
	"errors"
	"github.com/pborman/uuid"
	"github.com/vitelabs/go-vite/common/types"
	"github.com/vitelabs/go-vite/crypto/ed25519"
	"strconv"
)

const (
	keystoreVersion = 1
)

type Key struct {
	Id         uuid.UUID
	Address    types.Address
	PrivateKey *ed25519.PrivateKey
}

type encryptedKeyJSON struct {
	HexAddress string     `json:"hexaddress"`
	Id         string     `json:"id"`
	Crypto     cryptoJSON `json:"crypto"`
	Version    int        `json:"keystoreversion"`
	Timestamp  int64      `json:"timestamp"`
}

type cryptoJSON struct {
	CipherName   string       `json:"ciphername"`
	CipherText   string       `json:"ciphertext"`
	Nonce        string       `json:"nonce"`
	KDF          string       `json:"kdf"`
	ScryptParams scryptParams `json:"scryptparams"`
}

type scryptParams struct {
	N      int    `json:"n"`
	R      int    `json:"r"`
	P      int    `json:"p"`
	KeyLen int    `json:"keylen"`
	Salt   string `json:"salt"`
}

func (key *Key) Sign(data []byte) (signedData []byte, pubkey []byte, err error) {
	if l := len(*key.PrivateKey); l != ed25519.PrivateKeySize {
		return nil, nil, errors.New("ed25519: bad private key length: " + strconv.Itoa(l))
	}
	return ed25519.Sign(*key.PrivateKey, data), key.PrivateKey.PubByte(), nil
}

func newKeyFromEd25519(priv *ed25519.PrivateKey) *Key {
	return &Key{
		Id:         uuid.NewRandom(),
		Address:    types.PrikeyToAddress(*priv),
		PrivateKey: priv,
	}
}