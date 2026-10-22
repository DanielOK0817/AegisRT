import sys
from pathlib import Path
root=Path(__file__).resolve().parents[1]
sys.path.insert(0,str(root))
import pytest
raise SystemExit(pytest.main([str(p) for p in (root/'tests').glob('test_repro_*.py')]+sys.argv[1:]+['-q','-p','no:cacheprovider']))
