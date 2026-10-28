# ITEMBA NET release notes

## Crawler reliability and ArcoirisTV compatibility — 28 October 2026

**Revision:** `1ed95e0` (`Harden crawler shutdown and fix Arcoiris slash searches`)

### Summary

This update makes crawler shutdown safer and fixes ArcoirisTV searches for titles containing slashes. It also normalizes whitespace in generated ArcoirisTV search paths so equivalent titles produce canonical URLs.

### Changes

#### Graceful crawler shutdown

- The console and crawler roles now wait for the crawler goroutine to stop before closing PostgreSQL, Scout, and Vault resources.
- Shutdown waits up to 15 seconds for active crawler work to finish.
- Frontier completion writes still proceed after cancellation, but now have their own 15-second deadline so a database problem cannot block shutdown indefinitely.
- Regression tests cover clean shutdown, operation without a crawler, and the shutdown timeout fallback.

#### ArcoirisTV search compatibility

- Added the `path_words` driver-template filter. It treats slashes, backslashes, and whitespace as word separators before URL-path escaping.
- ArcoirisTV searches now use `path_words`, preventing encoded slashes (`%2F`) from producing HTTP 404 responses on the provider's search route.
- Spaces surrounding slashes collapse to one encoded space, avoiding duplicate non-canonical search URLs.
- Regression coverage checks both frontier URL generation and live search request construction.

### Validation

- `go test ./...` — passed.
- `go vet ./...` — passed.
- Linux binary build — passed.
- Live ArcoirisTV searches for `Joe Jackson/The Playhouse` and `No Way Out / Magnets` completed without the previous HTTP 404 failure.
- Post-deployment crawler status confirmed successful search cycles and newly seeded canonical ArcoirisTV URLs.

### Deployment

The update contains Go backend and YAML driver changes but no frontend changes or database migrations. Build a new Linux binary and restart the crawler so it loads both the code and the updated driver definition:

```powershell
$env:GOOS = "linux"; $env:GOARCH = "amd64"; go build -o itemba ./cmd/itemba
```

The Windows development loop documented in `README.md` remains unchanged. No configuration changes are required.

For rollback, restore the previous binary and `drivers/arcoiris-tv.yaml` together. Rolling back only the binary would leave the driver using a template filter that the older binary does not implement.

### Operational status and remaining work

The independent 72-hour soak was restarted with this revision at **10:22 EAT on 28 October 2026**, using the isolated `itemba_soak_20261028` database. It is scheduled to finish at **10:22 EAT on 31 October 2026**. Completion of that soak is not claimed by this release note.

Provider timeouts, DNS failures, and rate-limit backoffs remain under observation. They are operational failures rather than confirmed driver-contract regressions and should be addressed only where fresh evidence identifies a specific defect.
