# Fixture capability map

Intro prose is ignored by the parser. Only the one pipe table counts.

| capability | entrypoint | production_path | classification | owner | evidence |
| --- | --- | --- | --- | --- | --- |
| cap:alpha | cascade alpha run | cmd/cascade to the alpha subsystem | verified | - | `TestCapmap_AlphaRun` |
| cap:beta | rpc beta.get | daemon to the beta store | verified | - | TestCapmap_BetaGet, TestCapmap_BetaRefusal |
| cap:gamma | cascade gamma | present in tree, no probe yet | present-unverified | P2-ABC-01 | reviewed, no probe |
| cap:delta | rpc delta.set | not wired to the daemon | missing | NEW:P1-DELTA | no handler registered |
| cap:delta | cascade delta export | refused by policy as specified | policy-refused-as-specified | P3-XYZ-12 | refusal documented |
