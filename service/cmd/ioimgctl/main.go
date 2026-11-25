// Command ioimgctl builds and verifies signed ioimg URLs.
//
//	ioimgctl sign -base https://acme.ioimg.net -ops w_800,f_auto -source https://cdn.acme.com/a.jpg \
//	  -key kid1:secret -exp 24h
//	ioimgctl verify -url 'https://acme.ioimg.net/...' -key kid1:secret
package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ioimg/ioimg/service/pkg/ops"
	"github.com/ioimg/ioimg/service/pkg/sign"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "sign":
		signCmd(os.Args[2:])
	case "verify":
		verifyCmd(os.Args[2:])
	case "canonical":
		canonicalCmd(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println("ioimgctl 0.1.0")
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  ioimgctl sign    -source URL -ops OPS [-base URL] [-key kid:secret] [-exp 24h]
  ioimgctl verify  -url URL -key kid:secret
  ioimgctl canonical -source URL -ops OPS`)
	os.Exit(2)
}

func parseKeys(raw string) []sign.Key {
	keys, err := sign.ParseKeys(raw)
	if err != nil || len(keys) == 0 {
		fmt.Fprintln(os.Stderr, "invalid -key (want kid:secret):", err)
		os.Exit(2)
	}
	return keys
}

func signCmd(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	source := fs.String("source", "", "absolute source URL")
	opsRaw := fs.String("ops", "", "ops string, e.g. w_800,f_auto")
	base := fs.String("base", "http://localhost:8080", "ioimg base URL")
	key := fs.String("key", os.Getenv("IOIMG_SIGNING_KEYS"), "kid:secret[,kid2:secret2]")
	exp := fs.Duration("exp", 24*time.Hour, "expiry duration (0 = no exp)")
	includeExp := fs.Bool("include-exp", true, "include exp in the signed canonical string")
	_ = fs.Parse(args)
	if *source == "" || *opsRaw == "" {
		usage()
	}
	o, err := ops.Parse(*opsRaw, nil)
	if err != nil {
		fail("invalid ops: %v", err)
	}
	canonicalSource, err := sign.CanonicalSource(*source)
	if err != nil {
		fail("%v", err)
	}
	keys := parseKeys(*key)
	s := &sign.Signer{Keys: keys, IncludeExp: *includeExp}
	var expUnix int64
	if *exp > 0 {
		expUnix = time.Now().Add(*exp).Unix()
	}
	sig, err := s.Sign(o.Canonical(), canonicalSource, expUnix)
	if err != nil {
		fail("%v", err)
	}
	u := strings.TrimRight(*base, "/") + "/" + sig + "/" + o.Canonical() + "/" + canonicalSource
	if expUnix > 0 {
		sep := "?"
		if strings.Contains(canonicalSource, "?") {
			sep = "&"
		}
		u += sep + "exp=" + strconv.FormatInt(expUnix, 10)
	}
	fmt.Println(u)
}

func verifyCmd(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	raw := fs.String("url", "", "signed ioimg URL")
	key := fs.String("key", os.Getenv("IOIMG_SIGNING_KEYS"), "kid:secret")
	_ = fs.Parse(args)
	if *raw == "" {
		usage()
	}
	u, err := url.Parse(*raw)
	if err != nil {
		fail("invalid URL: %v", err)
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	idx := strings.Index(u.Path, "https://")
	if i := strings.Index(u.Path, "http://"); i >= 0 && (idx < 0 || i < idx) {
		idx = i
	}
	if idx < 0 {
		fail("source URL not found in path")
	}
	prefix := strings.TrimSuffix(u.Path[1:idx], "/")
	parts = strings.Split(prefix, "/")
	if len(parts) != 2 {
		fail("expected /{signature}/{ops}/{source}")
	}
	o, err := ops.Parse(parts[1], nil)
	if err != nil {
		fail("invalid ops: %v", err)
	}
	exp, _ := strconv.ParseInt(u.Query().Get("exp"), 10, 64)
	q := u.Query()
	q.Del("exp")
	q.Del("kid")
	q.Del("sig")
	sourceRaw := u.Path[idx:]
	if enc := q.Encode(); enc != "" {
		if strings.Contains(sourceRaw, "?") {
			sourceRaw += "&" + enc
		} else {
			sourceRaw += "?" + enc
		}
	}
	canonicalSource, err := sign.CanonicalSource(sourceRaw)
	if err != nil {
		fail("%v", err)
	}
	keys := parseKeys(*key)
	s := &sign.Signer{Keys: keys, IncludeExp: true}
	if err := s.Verify(parts[0], o.Canonical(), canonicalSource, exp); err != nil {
		fail("verify failed: %v", err)
	}
	fmt.Println("ok")
}

func canonicalCmd(args []string) {
	fs := flag.NewFlagSet("canonical", flag.ExitOnError)
	source := fs.String("source", "", "absolute source URL")
	opsRaw := fs.String("ops", "", "ops string")
	_ = fs.Parse(args)
	if *source == "" || *opsRaw == "" {
		usage()
	}
	o, err := ops.Parse(*opsRaw, nil)
	if err != nil {
		fail("invalid ops: %v", err)
	}
	cs, err := sign.CanonicalSource(*source)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("ops=%s\nsource=%s\n", o.Canonical(), cs)
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
