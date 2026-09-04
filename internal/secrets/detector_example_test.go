package secrets

// Purpose: the runnable godoc example for the detector's entry point
//
//	(Art.10.6 documentation deliverable for this ticket). It lives in its
//	own file because detector_test.go is already near the 300-line cap.
//
// Constraints: the example prints only metadata - class, location, name -
//
//	which is the whole API contract: there is no way to print the value
//	from a DetectionHit, because a hit does not carry one.

import "fmt"

// ExampleDetector_Scan shows the two outcomes that matter: a corroborated
// finding, reported by location and shape with a name to store it under,
// and an uncorroborated one that is reported but stays below the
// quarantine threshold.
func ExampleDetector_Scan() {
	detector, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	content := []byte("wifi password: 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0\nbuild 4a7c1e2f0b")
	for _, hit := range detector.Scan(content) {
		fmt.Printf("%s at %d+%d confidence %.2f -> %s\n",
			hit.Class, hit.Offset, hit.Len, hit.Confidence, hit.SuggestedName)
	}
	fmt.Println("quarantine-eligible:", len(detector.ScanCertain(content)))
	// Output:
	// high-entropy at 15+32 confidence 0.90 -> WIFI_PASSWORD
	// quarantine-eligible: 1
}
