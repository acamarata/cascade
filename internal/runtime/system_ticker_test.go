package runtime

import (
	"testing"
	"testing/synctest"
	"time"
)

// TestSystemTickerStopJoinsPump proves Stop joins even a delayed exit with an unread tick.
// Fake time and Wait establish blocked goroutines without scheduler polling.
func TestSystemTickerStopJoinsPump(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		systemTickerExitHook = func() { <-release }
		defer func() { systemTickerExitHook = nil }()
		ticker := NewSystemTicker(time.Second).(*systemTicker)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		returned := make(chan struct{})
		go func() {
			ticker.Stop()
			select {
			case <-ticker.done:
			default:
				t.Error("Stop returned before pump done closed")
			}
			close(returned)
		}()
		synctest.Wait()
		select {
		case <-returned:
			t.Error("Stop returned while pump exit was held")
		default:
		}
		close(release)
		synctest.Wait()
		select {
		case <-returned:
		default:
			t.Fatal("Stop did not return after pump exit was released")
		}
		select {
		case <-ticker.done:
		default:
			t.Error("pump still running after Stop")
		}
		ticker.Stop()
	})
}
