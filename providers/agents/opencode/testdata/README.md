# Native run fixture

This is a reconstructed replay fixture, not captured from a live run.
Live capture is blocked in the ticket sandbox: no network, external CLI,
daemon environment or vault access is permitted. Version, help and socket
evidence remain unverified. Do not use this fixture as live provenance.

The proposed compat credential row is `ZHIPU_API_KEY` for the Zhipu provider.
Its actual environment consumption must be confirmed during the live capture.
No credential value is present here. Tests inject a harmless fixture marker.

The three fixture types are step_start, text and step_finish. Fuzz seeds are
these lines plus malformed and unknown-type inputs, loaded by FuzzOpenCodeEvent.
The minimized equivalents in testdata/fuzz are reconstructed from this fixture,
not from a separate live recording.
The terminal fixture uses reason=stop; other finish reasons fail closed pending
recordings that establish their lifecycle meaning.

Required capture follow-up: use a throwaway HOME and CASCADE_HOME in
golang:1.26-bookworm, record the CLI version, `run --help`, argv and JSONL;
capture `lsof -nP -p <pid>` during the run, disclose every listening socket,
and submit it to per-spawn proxy review. Never persist the daemon's key.
