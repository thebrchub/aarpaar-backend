package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
)

func main() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}

	// Marshal private key to PKCS8
	privBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err)
	}
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	})

	// Marshal public key
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic(err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})

	privB64 := base64.StdEncoding.EncodeToString(privPEM)
	pubB64 := base64.StdEncoding.EncodeToString(pubPEM)

	// Write to a Go file that can be embedded
	os.WriteFile("tests/testutil/jwt_keys.go", []byte(
		"package testutil\n\n"+
			"// Auto-generated test RSA keys. DO NOT use in production.\n"+
			"const (\n"+
			"\tTestJWTPrivateKey = \""+privB64+"\"\n"+
			"\tTestJWTPublicKey  = \""+pubB64+"\"\n"+
			")\n",
	), 0644)
}
