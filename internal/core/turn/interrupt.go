// SPDX-License-Identifier: Apache-2.0
package turn

// Issuer is supplied by the control path, never inferred from input text.
type Issuer string

const (
	Person       Issuer = "person"
	Watchdog     Issuer = "watchdog"
	EpochLost    Issuer = "epoch_lost"
	GrantRevoked Issuer = "grant_revoked"
	Budget       Issuer = "budget"
	Shutdown     Issuer = "shutdown"
)

// Interrupt is the cancellation cause carried through a turn and its tools.
// Its text is derived solely from the issuer; it contains no external error.
type Interrupt struct{ Issuer Issuer }

func (i Interrupt) Error() string { return "turn_interrupted: " + string(i.Issuer) }
func (i Interrupt) Text() string {
	switch i.Issuer {
	case Person:
		return "You stopped this reply."
	case Watchdog:
		return "This reply stopped because it was no longer making progress."
	case EpochLost:
		return "This reply was interrupted by a restart. Please send a new message to continue."
	case GrantRevoked:
		return "This reply stopped because its permission was withdrawn."
	case Budget:
		return "This input is waiting because its automatic work allowance was reached."
	case Shutdown:
		return "This reply stopped because LINA shut down. Please send a new message to continue."
	default:
		return "This reply was interrupted. Please send a new message to continue."
	}
}
