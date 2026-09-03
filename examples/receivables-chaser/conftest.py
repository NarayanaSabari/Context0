"""Makes the `chaser` package importable regardless of where pytest is
invoked from, without installing it.
"""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
