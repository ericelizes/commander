The Commander harness uses beads_rust databases. Set `COMMANDER_EVAL_BR_BIN`
to an absolute path to the intended Rust executable before running harness
scenarios, for example `COMMANDER_EVAL_BR_BIN=/path/to/br-rust`. The harness
requires an executable reporting `br ...` from `--version`, logs its path,
version and SHA-256, and installs a private `br` launcher before inherited PATH.
It never discovers the dependency through the host `br` name, which may route
to another database backend. Missing or incompatible dependencies fail setup.
The injected executable is trusted test tooling; the version check is a
compatibility check, not authentication of arbitrary executable contents.

`TestEvalBRSelection` covers host fence shadowing, argument preservation,
unavailable dependency and incompatible version rejection. Existing
`activate-commander-explicit` and `activate-commander-pathless` scenarios cover
successful activation through this launcher with their normal assertions and
deadlines. Supply this dependency to CI harness invocations as well.
