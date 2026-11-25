package server

import (
	"os"

	"ioimg/internal/config"
	"ioimg/internal/purge"
)

func buildPurgers(c config.CDNConfig) []purge.Purger {
	var out []purge.Purger
	for _, name := range c.Providers {
		base := os.Getenv("IOIMG_CDN_" + upperSnake(name) + "_BASE")
		switch name {
		case "cloudflare":
			out = append(out, &purge.Cloudflare{
				Token: c.CloudflareToken, ZoneID: c.CloudflareZone,
				Options: purge.Options{BaseURL: base},
			})
		case "bunny":
			out = append(out, &purge.Bunny{Key: c.BunnyKey, Options: purge.Options{BaseURL: base}})
		case "fastly":
			out = append(out, &purge.Fastly{
				Token: c.FastlyToken, ServiceID: c.FastlyService,
				Options: purge.Options{BaseURL: base},
			})
		}
	}
	return out
}

func upperSnake(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
