package main

import "testing"

// The measured cap and the writable cap are not the same number. A profile
// that serves 32,768 tokens and reserves 8,192 for the answer can carry a
// 24,576-token packet and no more, whatever the model turns out to recall
// from — and Profile.Validate rejects the combination, so an unclamped write
// fails after the measurement rather than before it.
func TestCapToWriteLeavesRoomForTheAnswer(t *testing.T) {
	cases := []struct {
		name                        string
		measured, context, reserved int
		want                        int
		wantClamped                 bool
	}{
		{
			name: "recall exceeds what the window leaves",
			// The case this profile is actually in: recall held everywhere the
			// window allowed it to be tested.
			measured: 30000, context: 32768, reserved: 8192,
			want: 24576, wantClamped: true,
		},
		{
			name:     "recall stops first, which is the measurement worth having",
			measured: 12000, context: 32768, reserved: 8192,
			want: 12000, wantClamped: false,
		},
		{
			name:     "exactly the room available is not a clamp",
			measured: 24576, context: 32768, reserved: 8192,
			want: 24576, wantClamped: false,
		},
		{
			name: "a profile that reserves everything is left alone",
			// Nonsense configuration, but it must not silently become a
			// zero or negative packet cap.
			measured: 8000, context: 8192, reserved: 8192,
			want: 8000, wantClamped: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, clamped := capToWrite(tc.measured, tc.context, tc.reserved)
			if got != tc.want || clamped != tc.wantClamped {
				t.Errorf("capToWrite(%d, %d, %d) = %d, clamped=%v; want %d, clamped=%v",
					tc.measured, tc.context, tc.reserved, got, clamped, tc.want, tc.wantClamped)
			}
		})
	}
}
