"""Read the experiment VM host process power throttling state; no mutation."""
import ctypes
from ctypes import wintypes
import json
import sys

class State(ctypes.Structure):
    _fields_=[("Version",wintypes.ULONG),("ControlMask",wintypes.ULONG),("StateMask",wintypes.ULONG)]

k=ctypes.WinDLL("kernel32",use_last_error=True)
k.OpenProcess.argtypes=[wintypes.DWORD,wintypes.BOOL,wintypes.DWORD]
k.OpenProcess.restype=wintypes.HANDLE
k.GetProcessInformation.argtypes=[wintypes.HANDLE,ctypes.c_int,ctypes.c_void_p,wintypes.DWORD]
k.GetProcessInformation.restype=wintypes.BOOL
k.CloseHandle.argtypes=[wintypes.HANDLE]
for argument in sys.argv[1:]:
    pid=int(argument)
    handle=k.OpenProcess(0x1000,False,pid)
    if not handle:raise ctypes.WinError(ctypes.get_last_error())
    try:
        value=State(1,0,0)
        if not k.GetProcessInformation(handle,4,ctypes.byref(value),ctypes.sizeof(value)):
            raise ctypes.WinError(ctypes.get_last_error())
        print(json.dumps({"pid":pid,"version":value.Version,"control_mask":value.ControlMask,"state_mask":value.StateMask}))
    finally:k.CloseHandle(handle)
