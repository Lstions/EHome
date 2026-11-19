package firmwareid

import "testing"

// TestBuildProfile 钉住档位名的拼法：它是包清单 identity.profile 与镜像
// 交叉核对（INV-3）的唯一实现，拼错了会把「清单与镜像错配」判成通过。
func TestBuildProfile(t *testing.T) {
	cases := []struct {
		chip    string
		flashMB uint16
		psram   bool
		want    string
	}{
		{ChipC6, 8, false, "c6-n8"},
		{ChipC6, 16, false, "c6-n16"},
		{ChipS3, 16, false, "s3-n16"},
		{ChipS3, 16, true, "s3p-n16"},
		{ChipS3, 8, true, "s3p-n8"},
		{"", 16, false, ""},
		{"esp32", 16, false, ""},
		{ChipS3, 0, false, ""},
	}
	for _, tc := range cases {
		if got := BuildProfile(tc.chip, tc.flashMB, tc.psram); got != tc.want {
			t.Fatalf("BuildProfile(%q,%d,%v) = %q, want %q", tc.chip, tc.flashMB, tc.psram, got, tc.want)
		}
	}
}
