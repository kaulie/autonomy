package autonomy_test

// Link the database module's engines into the test binary so the sqlite engine
// registers itself in the root package's engine registry, exactly as cmd's
// blank import does for the running process.
import _ "github.com/kaulie/autonomy/src/db"
