// fundsign：模拟资金系统的离线 Ed25519 签名（方案 6.8）。仅开发环境使用；生产由资金系统在离线机器上签名。
//
//	go run ./cmd/fundsign keygen                       # 生成密钥对（seed 与公钥 Base64）
//	go run ./cmd/fundsign sign -seed <hex> -network MOCK -address MOCKADDR1 -days 365
//	  → 输出可直接执行的 INSERT INTO receive_addresses ...
//
// 签名原文：yuhai-addr-v1|{network}|{address}|{valid_until_unix}
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: fundsign keygen | sign")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		seed := make([]byte, 32)
		_, _ = rand.Read(seed)
		priv := ed25519.NewKeyFromSeed(seed)
		fmt.Printf("FUND_SEED_HEX=%s\nFUND_ADDRESS_PUBKEY_B64=%s\n", hex.EncodeToString(seed), base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	case "sign":
		fs := flag.NewFlagSet("sign", flag.ExitOnError)
		seedHex := fs.String("seed", "", "Ed25519 seed hex")
		network := fs.String("network", "MOCK", "TRON | MOCK")
		address := fs.String("address", "", "收款地址")
		keyID := fs.String("key-id", "fund-dev-1", "密钥标识")
		days := fs.Int("days", 365, "有效天数")
		_ = fs.Parse(os.Args[2:])
		seed, err := hex.DecodeString(*seedHex)
		if err != nil || len(seed) != 32 || *address == "" {
			fmt.Println("需要 -seed(32 字节 hex) 与 -address")
			os.Exit(2)
		}
		priv := ed25519.NewKeyFromSeed(seed)
		until := time.Now().Add(time.Duration(*days) * 24 * time.Hour).UTC()
		msg := fmt.Sprintf("yuhai-addr-v1|%s|%s|%d", *network, *address, until.Unix())
		sig := base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(msg)))
		fmt.Printf("INSERT INTO receive_addresses (network, address, label, signature, sig_key_id, valid_until, status)\nVALUES ('%s', '%s', 'dev', '%s', '%s', to_timestamp(%d), 'ACTIVE');\n",
			*network, *address, sig, *keyID, until.Unix())
	}
}
