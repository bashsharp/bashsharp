# go-runewidth fork

This is the MIT-licensed `github.com/mattn/go-runewidth` v0.0.27 source,
including its upstream checksum tests. The only behavior change is deferred
construction of its merged Unicode width tables. A `sync.Once` on the first
`Condition.RuneWidth` call constructs the same tables that upstream constructs
during package initialization. The locale-derived `EastAsianWidth` setting is
still initialized at package load, so TUI width results are unchanged.

The fork exists because bashy links the TUI graph into the same executable used
for every shell, utility, and background job carrier. Its 1.1 million rune
strict-width lookup table cost about 39 ms on the Linux certification host at
every process start, including jobs that never ask for a width.

Bashsharp carries this copy from yoke commit
`99ef1333` (external: defer runewidth table construction until first width query).
Only its module path changes to `github.com/bashsharp/bashsharp/external/runewidth`.
The standalone bsh module replaces upstream runewidth with this local directory
so shell startup avoids table construction without linking any yoke package.
The upstream MIT license is preserved in LICENSE.
