# Third-party components and data

Runtime Go dependencies are pinned in `go.mod` / `go.sum`; frontend dependencies
are pinned in `web/pnpm-lock.yaml`. Their respective licenses continue to apply.

Primary components include Go's HTTP stack (BSD), Pebble (BSD), quic-go (MIT),
DuckDB and its Go bindings (MIT), Huma (MIT), SCS (MIT), React (MIT), Vite
(MIT), and Apache ECharts (Apache-2.0). Consult the resolved dependency source
for the authoritative license and notices of each pinned release.

## Geographic data

GeoIP databases are supplied by the operator, not bundled. The MMDB reader is
open source; that does not change the database provider's download, update, or
redistribution license. GeoLite2 can be obtained from MaxMind with an account.

The UI world map uses simplified Natural Earth geography distributed through
`world-atlas`. Natural Earth data is public domain. The world-atlas package and
TopoJSON conversion code have their own licenses; see their package metadata.
Map data is served locally, not fetched from an external tile or analytics service.
`d3-geo` supplies the spherical projection stream and antimeridian clipping (ISC;
its bundled GeographicLib code carries the included MIT notice). Notices ship in
`web/public/licenses.txt`.
