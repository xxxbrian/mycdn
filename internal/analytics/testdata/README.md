# Local GeoIP verification fixtures

`GeoIP2-Country-Test.mmdb` and `GeoLite2-ASN-Test.mmdb` are unmodified test
databases from [maxmind/MaxMind-DB](https://github.com/maxmind/MaxMind-DB), commit
`0ee49278e35fd7bdf341ac236edbba20de1aceb9`, `test-data/`. They are distributed
under that repository's MIT license, reproduced in `LICENSE-MIT` (MaxMind, Inc.).

These are synthetic upstream **test** databases, not production GeoLite data.
Tests run entirely offline; no downloader or production MMDB is bundled.
