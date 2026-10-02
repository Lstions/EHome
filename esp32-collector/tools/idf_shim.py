"""Run ESP-IDF's idf.py with MSYSTEM removed from the environment.

Why this file exists
--------------------
ESP-IDF 6.1's tools/idf.py ends with:

    if __name__ == '__main__':
        try:
            if 'MSYSTEM' in os.environ:
                print_warning(
                    'MSys/Mingw is no longer supported. Please follow the getting '
                    'started guide of the documentation in order to set up a '
                    'suitiable environment, or continue at your own risk.'
                )
            elif os.name == 'posix' and not _valid_unicode_config():
                ...
            elif os.name == 'nt' and not _windows_unicode_satisfactory():
                ...
                main()
            else:
                main()
        except FatalError as e:
            ...

The MSYSTEM branch prints a warning and then falls off the end of the module
WITHOUT calling main().  It does not raise, it does not exit non-zero: idf.py
simply exits 0 having done nothing at all.

MSYSTEM is set by every MSys/Git-Bash session (e.g. MSYSTEM=UCRT64), and Windows
MSys re-injects it into every child process, so `env -u MSYSTEM python idf.py`
does NOT clear it -- the variable is back inside the interpreter.  Verified on
this workstation:

    $ env -u MSYSTEM python -c "import os; print(os.environ.get('MSYSTEM'))"
    UCRT64

The only place the variable can be dropped is inside the Python process itself.
This wrapper does exactly that and then executes idf.py in-process, so the
normal code path runs.

Cost of not having it: a build launched from Git-Bash reports success while
producing no image.  build_firmware.sh can only detect that afterwards, and the
error surfaces at the post-link IRAM gate ("no such ELF") rather than at the
build.  That is the failure mode this file removes.

Usage (build_firmware.sh does this for you):

    "<idf python>" tools/idf_shim.py --project-dir ... build
"""

import os
import sys

_IDF_PATH = os.environ.get("IDF_PATH", "").replace("\\", "/")
_IDF_PY = _IDF_PATH + "/tools/idf.py"

if not _IDF_PATH or not os.path.isfile(_IDF_PY):
    sys.stderr.write(
        "idf_shim: IDF_PATH is not set to a usable ESP-IDF checkout "
        "(got %r); expected %s\n" % (os.environ.get("IDF_PATH"), _IDF_PY)
    )
    raise SystemExit(127)

# Both are set by MSys shells; neither is meaningful to ESP-IDF's build.
os.environ.pop("MSYSTEM", None)
os.environ.pop("MSYS", None)

# idf.py imports its own siblings (python_version_checker, idf_py_actions, ...)
# by plain module name, so its directory has to be importable.
sys.path.insert(0, os.path.dirname(_IDF_PY))
sys.argv[0] = _IDF_PY

with open(_IDF_PY, "rb") as _f:
    _source = _f.read()

# idf.py is a script, not a module: run it as __main__ so its
# `if __name__ == '__main__':` block executes.
exec(compile(_source, _IDF_PY, "exec"), {"__name__": "__main__", "__file__": _IDF_PY})
