# Testing

Every push runs, on a GitHub Linux runner:

```sh
go vet ./...
go test -race -count=1 ./...
node --test scripts/test-web.mjs # app.js with a simulated DOM and fetch
python3 scripts/test-start.py   # start.sh against fake docker and rc-service
docker compose config --quiet
docker build .                  # also runs the test suite inside the build
```

The image is published only after all of these pass.

The Go tests use synthetic ESXi output in `fixtures/`, a fake host for the
migration engine, a real local SSH server for the transport and a real POSIX
shell for the detached clone worker.
