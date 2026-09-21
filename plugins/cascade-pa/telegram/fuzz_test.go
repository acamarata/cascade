package telegram

// Purpose (this file): FuzzTelegramUpdate — the decoder fuzz target 06 §5
//   rule 7 requires for the one boundary in this package that parses arbitrary
//   external bytes.
//
// It fuzzes the WHOLE inbound path's pure half, not just json.Unmarshal: the
// decoded update is run through every classifier the dispatch gate consults
// (media presence, pair-command parse, verb candidates), because a fuzz target
// that stops at Unmarshal cannot find a dispatch-classification bypass.
//
// Corpus: plugins/cascade-pa/telegram/testdata/fuzz/FuzzTelegramUpdate/
//   (R-21.266 — package-local, this check names exactly one package).
//
// SPORT: plugins/cascade-pa/telegram FuzzTelegramUpdate/ADDED
//   (P1-E23-W5-S48-T1).

import (
	"encoding/json"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func FuzzTelegramUpdate(f *testing.F) {
	f.Add([]byte(`{"update_id":1,"message":{"message_id":1,"chat":{"id":1,"type":"private"},"date":1,"text":"hi"}}`))
	f.Add([]byte(`{"update_id":2,"callback_query":{"id":"c1","from":{"id":2,"is_bot":false,"first_name":"x"},"data":"/enroll w"}}`))
	f.Add([]byte(`{"update_id":3,"message":{"message_id":3,"chat":{"id":3},"photo":[{"file_id":"x"}]}}`))
	f.Add([]byte(`{"update_id":4,"callback_query":{"id":"c2","from":{"id":4},"message":{"message_id":9,"chat":{"id":4},"date":0}}}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(``))
	f.Add([]byte(`{`))
	f.Fuzz(func(_ *testing.T, raw []byte) {
		var u Update
		if err := json.Unmarshal(raw, &u); err != nil {
			return
		}
		// Every classifier the dispatch gate reaches for, on whatever the
		// decoder produced. None of them may panic on any byte sequence.
		if u.Message != nil {
			_ = u.Message.HasRefusedMedia()
			_, _ = isPairCommand(u.Message.Text)
			_ = cascadepa.RefusesElevated(nodeVerbPolicy(), u.Message.Text, false)
			_ = cascadepa.VerbCandidates(u.Message.Text, false)
		}
		if u.CallbackQuery != nil {
			_ = cascadepa.RefusesElevated(nodeVerbPolicy(), u.CallbackQuery.Data, true)
			_ = cascadepa.VerbCandidates(u.CallbackQuery.Data, true)
			if u.CallbackQuery.Message != nil {
				_ = u.CallbackQuery.Message.HasRefusedMedia()
			}
		}
		_ = stampInbound(u)
	})
}
