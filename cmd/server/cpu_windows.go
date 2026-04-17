//go:build windows

package main

// getCPUPercent on Windows always returns 0.
// syscall.Getrusage is not available on Windows; a proper implementation
// would use the Win32 GetProcessTimes API. Left as a future enhancement.
func (s *Server) getCPUPercent() float64 {
	return 0
}
