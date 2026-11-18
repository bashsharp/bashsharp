package shellrt

// ExpansionAbort aborts only the shell statement whose argument expansion
// failed. The next source statement can run and replace its exit status.
type ExpansionAbort struct{}

func (ExpansionAbort) Error() string   { return "bad substitution" }
func (ExpansionAbort) ExitStatus() int { return 1 }
func BadSubstitutionValue() string     { panic(ExpansionAbort{}) }
func TryShellStatement(body func()) (err error) {
	defer func() {
		if value := recover(); value != nil {
			if failure, ok := value.(ExpansionAbort); ok {
				err = failure
				return
			}
			panic(value)
		}
	}()
	body()
	return nil
}
