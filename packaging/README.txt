defy-installer
==============

defy-installer puts ZMK on a Dygma Defy wireless keyboard, keeps it up
to date, and puts Dygma's own firmware back. You run it in a terminal.

This folder holds:

  defy-installer          the installer, defy-installer.exe on Windows
  defy-installer.yaml     its settings
  README.txt              this file
  LICENSE                 the installer's licence, MIT
  THIRD_PARTY_LICENSES    the licences of the Go code built into it

Keep these files together in one folder.


1. Unpack the download
----------------------

macOS: double-click defy-installer-macos.zip in Finder.

Windows: right-click the zip, choose Extract All, then Extract. Run the
installer from the extracted folder. It does not work from inside the
zip.

Linux: unpack it with tar, for example:

  tar xzf defy-installer-linux-x64.tar.gz

Each gives you a folder named defy-installer.


2. Open a terminal in the folder
--------------------------------

macOS: open Terminal, which is in Applications, Utilities. Type cd and a
space, drag the defy-installer folder onto the Terminal window, and
press Return.

Windows: open the defy-installer folder in File Explorer. Click the
address bar at the top, type powershell and press Enter.

Linux: right-click inside the folder and choose Open in Terminal, or cd
to it.


3. Install ZMK
--------------

Installing ZMK erases the layout stored on the keyboard, so back it up
in Bazecor first. Then quit Bazecor, which holds the keyboard's port
while it runs.

Plug the Neuron into this computer with its USB cable, and run:

  macOS and Linux:   ./defy-installer install
  Windows:           .\defy-installer.exe install

The installer tells you what to do at each step, such as when to plug
in the halves' cables and when to switch the halves on.

Other commands, run the same way, such as ./defy-installer update:

  update      update ZMK, keeping your Bluetooth pairings
  uninstall   put Dygma's 2.3.0 firmware back
  status      show what the keyboard runs, whether the halves are
              connected, and their batteries
  help        show these commands, and where to find the others

Never update the firmware with Bazecor while the keyboard runs ZMK.
After Bazecor's write, neither ZMK nor Dygma's firmware starts. To go
back to Dygma's firmware, run uninstall.


The first run on macOS
----------------------

The installer is not signed with an Apple Developer ID, so the first
time you run it, macOS may stop it with a message that says
"defy-installer" Not Opened. Click Done, then:

1. Open System Settings, then Privacy & Security.
2. Scroll down to Security. Next to the line that says defy-installer
   was blocked, click Open Anyway, and confirm with your password or
   Touch ID.
3. Run the command again. When macOS asks once more, click Open Anyway.

You do this once for each new version of the installer. Instead of the
three steps, you can also clear the block in the terminal before the
first run:

  xattr -d com.apple.quarantine defy-installer


The first run on Windows
------------------------

The installer is not signed, so Windows may show "Windows protected your
PC". Click More info, then Run anyway.


Linux: permission to use the keyboard's ports
---------------------------------------------

If the installer says "Linux does not let you open the keyboard's port",
add yourself to the group that owns the keyboard's serial ports, then
log out and back in. On most Linux systems that group is dialout:

  sudo usermod -aG dialout "$USER"

On Arch Linux it is uucp:

  sudo usermod -aG uucp "$USER"

To run it once without joining the group, start it with sudo, from this
folder so that it uses the settings file here:

  sudo ./defy-installer update

A settings file it changes under sudo then belongs to root.


Settings
--------

defy-installer.yaml says which keymap the installer puts on the
keyboard: the default layout, your own keymap repository on GitHub, or
firmware files on this computer. Comments in the file explain each
setting, and any text editor can change it. Most people never need to,
since this command saves your keymap repository in the file for every
later update:

  ./defy-installer update --from yourname/your-repo

To go back to the default layout:

  ./defy-installer update --from default

If you write a Windows path into the file by hand, put it in single
quotes:

  firmware: 'C:\Users\alice\Downloads\firmware.zip'

In double quotes a backslash starts an escape, so "C:\new" would read as
C:, a line break and ew.

The installer reads the settings file in its own folder. A new version
of the installer comes with a new settings file, with the keymap left
empty. An empty keymap keeps the one the keyboard runs now, so unpacking
a new version does not change your keymap.


More information
----------------

The full README covers checking the download, building the installer
yourself, and the advanced commands:

  https://github.com/borovaka/defy-installer#readme

Report problems at https://github.com/borovaka/defy-installer/issues
