package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"esxi-mover/internal/migration"
	"esxi-mover/internal/web"
)

func main() {
	listen := flag.String("listen", ":8443", "HTTPS listening address")
	applianceUUID := flag.String("appliance-uuid", os.Getenv("MOVER_APPLIANCE_UUID"), "local appliance BIOS UUID for self-migration protection")
	flag.Parse()
	cert, fingerprint, e := web.EphemeralTLS()
	if e != nil {
		log.Fatal("Cannot create ephemeral TLS certificate")
	}
	token, shown := adminToken(os.Getenv("MOVER_ADMIN_TOKEN"))
	options := migration.DefaultOptions()
	options.ApplianceUUID = *applianceUUID
	server := &http.Server{Addr: *listen, Handler: web.New(token, options).Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 15 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}}}
	host, port, _ := net.SplitHostPort(*listen)
	if host == "" {
		host = "<appliance-ip>"
	}
	fmt.Printf("ESXi Mover admin token: %s\nHTTPS certificate SHA256: %s\nOpen https://%s\nCredentials and application state stay in RAM. Source VM files are never deleted.\n", shown, fingerprint, net.JoinHostPort(host, port))
	if e = server.ListenAndServeTLS("", ""); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}

// adminToken uses the operator's token when one is set, so a container started
// in the background can be signed into without reading its log. It is never
// echoed: the operator already has it.
func adminToken(supplied string) (token, shown string) {
	if supplied == "" {
		t := migration.NewID() + migration.NewID()
		return t, t
	}
	return supplied, "(set from MOVER_ADMIN_TOKEN)"
}
