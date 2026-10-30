# defy-installer

defy-installer installs [zmk-defy-wireless](https://github.com/borovaka/zmk-defy-wireless), a ZMK port for the Dygma Defy wireless, keeps it up to date and puts Dygma's firmware back. It writes the Neuron, the Defy's wireless receiver, which holds the keymap. It writes the halves through the Neuron. It downloads what it installs and tells you, step by step, when to plug or unplug a cable or switch a half on or off. Each OS gets one program, with no runtime dependencies.

This is a community project, not affiliated with or endorsed by Dygma. It rewrites the keyboard's firmware. You use it at your own risk, without warranty; see the [licence](#licence).

It has written the Neuron and both halves of one keyboard on macOS, on Windows and on Linux. On macOS it has also taken that keyboard back to Dygma's firmware and installed ZMK again.

## Quick start

On macOS or Linux, in a terminal:

```sh
curl -fsSL https://github.com/borovaka/defy-installer/releases/latest/download/install.sh | sh
```

On Windows, in PowerShell:

```powershell
irm https://github.com/borovaka/defy-installer/releases/latest/download/install.ps1 | iex
```

The script picks the archive for your computer, checks it against the release's `SHA256SUMS` and unpacks it into a `defy-installer` folder in your home folder. It keeps the settings file of an earlier install there, runs nothing as administrator, and prints what to run next. Read it first if you like: [install.sh](packaging/install.sh) and [install.ps1](packaging/install.ps1). Then, from that folder:

| Command | What it does |
| --- | --- |
| `./defy-installer install` | Installs ZMK on a keyboard that runs Dygma's firmware |
| `./defy-installer update` | Installs what changed since the last install or update |
| `./defy-installer status` | Shows what the Neuron and each half run |
| `./defy-installer uninstall` | Puts Dygma's 2.3.0 firmware back |

On Windows the program is `.\defy-installer.exe`. The sections below cover a download by hand and each command.

## Installing

The one-line commands in the [quick start](#quick-start) do these steps for you. They download the archive for your computer and the release's `SHA256SUMS`, stop if the archive's SHA-256 does not match its line there, and unpack it into `defy-installer` in your home folder. A `defy-installer.yaml` already there is kept and every other file is replaced, so running the command again updates the program. Set `DEFY_INSTALLER_DIR` to install into another folder. The scripts never run `sudo`, and run the program only to print its version.

To do it by hand, download the archive for your computer from the [releases](https://github.com/borovaka/defy-installer/releases):

| Computer | File |
| --- | --- |
| Mac, Apple silicon or Intel | `defy-installer-macos.zip` |
| Windows | `defy-installer-windows.zip` |
| Windows on Arm | `defy-installer-windows-arm64.zip` |
| Linux on x86-64 | `defy-installer-linux-x64.tar.gz` |
| Linux on 64-bit Arm | `defy-installer-linux-arm64.tar.gz` |

Unpack it: double-click the zip on macOS, use Extract All on Windows, or `tar xzf` on Linux. You get a `defy-installer` folder with the program, its settings file `defy-installer.yaml`, a `README.txt` and the licences. Keep them together. On Windows, run the program from the extracted folder, not from inside the zip.

Open a terminal in that folder: on macOS, type `cd ` in Terminal and drag the folder onto the window; on Windows, type `powershell` in File Explorer's address bar. Then run the program as `./defy-installer` on macOS and Linux, or `.\defy-installer.exe` on Windows. The examples below write it as `defy-installer`.

The first run needs one more step on some systems. After the one-line commands, the macOS and Windows steps are not needed: curl and PowerShell's `Invoke-WebRequest` do not mark what they download, so neither system checks the program on its first run. The Linux step still applies.

- **macOS.** The program is not signed with an Apple Developer ID, so macOS stops it the first time. Open System Settings, then Privacy & Security, click Open Anyway next to the message about `defy-installer`, and run it again. Or clear the download mark first: `xattr -d com.apple.quarantine defy-installer`.
- **Windows.** If Windows shows "Windows protected your PC", click More info, then Run anyway.
- **Linux.** The keyboard's serial ports, `/dev/ttyACM*`, belong to the `dialout` group, or to `uucp` on Arch Linux. Add yourself with `sudo usermod -aG dialout "$USER"`, or `uucp` in its place, then log out and in. Until then the installer stops with that advice when it needs a port. To run it once without joining the group, start it with `sudo`, as in `sudo ./defy-installer update`. Under `sudo` it uses the settings file beside the program, or root's in `/root/.config/defy-installer/` when there is none there, and a settings file it writes for `--from` or `--firmware` then belongs to root.

To check the download, compare it with the release's `SHA256SUMS`: `shasum -a 256 -c SHA256SUMS --ignore-missing` on macOS, `sha256sum -c SHA256SUMS --ignore-missing` on Linux, `Get-FileHash` in PowerShell. Each archive also has a build provenance attestation, which shows that the release workflow built it from this repository: `gh attestation verify defy-installer-macos.zip --repo borovaka/defy-installer`. `install.sh` and `install.ps1` are release files too, and `SHA256SUMS` lists them.

## Using it

```sh
defy-installer install      # install ZMK
defy-installer update       # update ZMK, keeping your Bluetooth pairings
defy-installer uninstall    # put Dygma's firmware back
defy-installer status       # what the keyboard runs, the halves' links and batteries
```

Quit Bazecor, and close ZMK Studio in a browser, before you start. Either holds the keyboard's serial port while it runs. Never let Bazecor write the Neuron while it runs ZMK. Bazecor writes only from `0x27000`, so it cannot put back S140, the Bluetooth stack that Dygma's firmware runs on. ZMK takes S140's place, so after a Bazecor write neither ZMK nor Dygma's firmware starts.

`install` and `update` do whatever the keyboard needs, whichever you type. Both download the firmware from GitHub and check it before they write anything.

- **From Dygma's firmware**, they install ZMK. Back up your layout with Bazecor first. Dygma's firmware erases its settings, which hold the layout, as it enters its bootloader. The installer asks for both halves to be cabled to the Neuron and switched off. If the left half still runs Dygma's firmware, Dygma's firmware then waits for one press of Esc on that half before the update, as in Bazecor's update, and the installer says when to press it. The installer writes the Neuron and both halves, then waits, with the cables in, until both halves have installed their radios and paired with the Neuron over Bluetooth. If a half has not connected after two minutes, it asks you to unplug that half's cable, switch it off, wait 5 seconds and switch it on. Then type with the cables in, or unplug them and switch the halves on. Pair your computers over Bluetooth again afterwards, since ZMK keeps pairings of its own.
- **On ZMK**, they update. The Neuron and each half report what they run, so the installer writes only what has changed. After a keymap change that is the Neuron alone, with no cables to move. When the halves need new firmware too, it asks for the cables and writes them first. Your Bluetooth pairings stay.
- **A Neuron left in its bootloader** by a write that did not finish is written again, which clears its settings, and then the halves are written.

Every write of the Neuron by `install` or `update` clears the keymap changes made in ZMK Studio, so that the keymap you install takes effect, and says so before it writes. Studio is for trying a layout; a layout you want to keep belongs in your own keymap repository.

`--yes` answers yes to the questions, such as whether you backed up your layout. It does not write a keyboard that is already up to date again, and steps that need your hands still wait for Enter. `--verbose` prints the detail of each step, such as ports, addresses and CRCs.

### Your own keymap

Make your keymap repository from the [Defy-Wireless-ZMK](https://github.com/borovaka/Defy-Wireless-ZMK) template. Each push to its default branch builds the firmware and puts it in the repository's release tagged `firmware`. Then:

```sh
defy-installer update --from yourname/your-repo     # the first time
defy-installer update                               # after each later push
defy-installer update --from default                # back to the default layout
```

The installer saves `--from` in its settings file once the keyboard runs that build, so later updates follow your repository. The installer can download only from public repositories. For a private one, or a build on your computer, give the files:

```sh
defy-installer update --firmware firmware.zip   # the run's artifact, as GitHub serves it
defy-installer update --firmware firmware       # a folder, such as build.sh's output
```

`--firmware` is saved too, and while it is, `update` installs those files and does not look at GitHub; it says so each time. `--from` with a repository again changes that.

### Settings

`defy-installer.yaml`, next to the program, holds two settings, with comments that explain them:

- `keymap`: `default`, or your repository as `owner/repo`. A tag after `@`, such as `default@v0.1.0`, stays on that release, for example to go back after a bad one. Left empty, the installer keeps the keymap the keyboard runs now, so a new version of the installer, which comes with a fresh settings file, never switches your keymap.
- `firmware`: a zip or folder on this computer, used instead of `keymap` when set.

`--from` and `--firmware` change the file and leave your comments and other lines as they are. Without a settings file next to the program, the installer uses `defy-installer.yaml` in your user settings folder: `~/Library/Application Support/defy-installer/` on macOS, `%AppData%\defy-installer\` on Windows, `~/.config/defy-installer/` on Linux. `status` shows which file it uses.

### Going back to Dygma's firmware

```sh
defy-installer uninstall
```

`uninstall` downloads Dygma's 2.3.0 files from [Dygma's firmware repository](https://github.com/Dygmalab/Firmware-release) and checks each against its pinned SHA-256 before it writes anything: `Wireless_neuron.hex` and `keyscanner.bin` from the [Defy v2.3.0 release](https://github.com/Dygmalab/Firmware-release/releases/tag/Defy_v2.3.0), and S140, `n2_s140_nrf52_7.2.0_softdevice.hex`, from the repository's `Defy/` folder at a fixed commit. With the halves cabled and switched off, it writes the Neuron back, then both halves through Dygma's Neuron firmware, as Bazecor's update does. Then unplug the Neuron and plug it back in, and switch each half on with its cable out. Restore your layout from your Bazecor backup.

Without the network, download the three files into a folder yourself and run `defy-installer uninstall --firmware <folder>`.

## Advanced commands

`defy-installer advanced`, or `adv`, holds the commands the guided ones are built from. Each is a single step, with no guidance and no downloads:

```sh
defy-installer advanced neuron write --hex neuron-hybrid.hex --keep-settings
defy-installer advanced halves write --left left-hybrid-radio.bin --right right-hybrid-radio.bin
defy-installer advanced halves probe          # read the split pairing and both halves' factory bootloaders
defy-installer advanced neuron bootloader     # enter and identify the bootloader; writes nothing
defy-installer advanced neuron start          # start the firmware from the bootloader
defy-installer advanced neuron check-s140 --s140 n2_s140_nrf52_7.2.0_softdevice.hex
defy-installer advanced neuron stock --hex Wireless_neuron.hex --s140 n2_s140_nrf52_7.2.0_softdevice.hex
defy-installer advanced stock --files ./dygma-2.3.0
```

All of them find the one Neuron connected. With more than one, choose with `--serial`, the Neuron's USB serial number, or `--port`; `defy-installer debug ports` lists both.

### Writing the Neuron

When the Neuron runs ZMK, `advanced neuron write` enters the Neuron's bootloader through its Studio port, the serial port that ZMK Studio uses to edit the keymap. It writes the image, starts it and waits for ZMK to come back. `--keep-settings` keeps the pairings, host profiles and Studio changes at `0x6C000..0x72000`; leave it out for a first install, which clears them. Unlike `update`, it does not clear the Studio changes.

From Dygma's firmware, it asks Dygma's firmware for its bootloader over Focus, Bazecor's serial protocol, with the commands Bazecor sends: `upgrade.start`, then `upgrade.isReady`, then `upgrade.neuron`. Dygma's firmware erases its own settings, which hold the layout, as it enters. If the left half is cabled and runs Dygma's firmware, Dygma's firmware waits for that half's top-left Esc key before the update. With the halves' cables out it does not wait.

The Neuron's button also enters the bootloader, and keeps Dygma's settings. Of the Neuron's two buttons, it is the one farther from the USB socket. Take the halves' cables out and unplug the Neuron, then hold the button while plugging the Neuron back in, and release it after about 5 seconds. `advanced neuron write` then finds the bootloader. Entered with the button, the bootloader acknowledges the request to start the firmware but stays in the bootloader, so unplug the Neuron and plug it back in instead of running `advanced neuron start`. From Dygma's firmware, `advanced neuron bootloader`, `check-s140` and `neuron stock` need the button; only `neuron write` asks Dygma's firmware for its bootloader.

`advanced neuron stock` puts back only the Neuron's firmware and S140; `advanced stock --files` does the whole keyboard from a folder of Dygma's three files. `advanced neuron check-s140` compares S140 on the Neuron with Dygma's file, page by page, without writing. On a Neuron running ZMK, which takes S140's place, it always reports a difference.

### Writing the halves

`advanced halves write` needs both halves cabled to the Neuron. Switch them off as well: the write goes over the cables and works with a half switched on, but a half switched off runs from its cable alone, so it restarts cleanly if the Neuron loses power partway. It finds the half updater among the Neuron's two USB serial ports: the port through which ZMK on the Neuron writes the halves. Typing pauses while it runs. Each half installs the radio firmware from its `-hybrid-radio` image as it starts; allow about half a minute before checking typing.

If the Neuron is not paired with both halves, as after a Neuron write without `--keep-settings`, `advanced halves write` resets the split pairing before it writes. The Neuron clears its split bonds and both halves' settings, and each half pairs again as a new device, with its cable in or out. The Neuron's pairings with computers stay. `--reset-pairing` forces the reset. See [split pairing](https://github.com/borovaka/zmk-defy-wireless/blob/main/docs/half-updater.md#split-pairing).

### Interrupting

The first Ctrl-C stops a command between two steps, such as two pages of a write, never in the middle of one. The installer says so, and the exit status is then 130. A second Ctrl-C quits at once, which can leave a write half done.

An interrupted Neuron write leaves the Neuron waiting in its bootloader. Keep it plugged in and run the command that `install` or `update` prints, which names the build with `--from` or `--firmware`. That writes the whole Neuron again, which clears its Bluetooth pairings and any changes made in ZMK Studio, even after an interrupted `update`. Unplugged, the Neuron needs its button to reach the bootloader again. An interrupted halves write releases the halves, so write both again.

## Troubleshooting

`defy-installer debug` holds read-only commands, safe at any time:

```sh
defy-installer debug ports           # USB serial ports, and which are the Neuron's
defy-installer debug ports --json
defy-installer debug check --neuron neuron-hybrid.hex
defy-installer debug check --left left-hybrid-radio.bin --right right-hybrid-radio.bin
defy-installer debug check --stock-neuron Wireless_neuron.hex --s140 n2_s140_nrf52_7.2.0_softdevice.hex
defy-installer debug check --keyscanner keyscanner.bin
```

`debug check` checks firmware files without a keyboard. On one kind of file, it runs the checks a write runs before it opens a port:

- A ZMK Neuron image is Intel HEX within `0x1000..0x72000`, with a vector table at `0x1000` whose stack pointer lies in RAM and whose reset vector points into the image. `check` says whether it ends before the ZMK settings at `0x6C000`, so that an update can keep them.
- A half's package has a valid seal and program CRC, vectors inside the program, and metadata for the side it is given as.
- Dygma's 2.3.0 files must match their pinned SHA-256; see [zmk-defy-wireless's Neuron flash notes](https://github.com/borovaka/zmk-defy-wireless/blob/main/docs/neuron-flash.md#back-to-stock).

A build's `manifest.json` lists each image's size and SHA-256, and for the halves the program CRC32 from its seal; `install` and `update` check every downloaded image against it. See zmk-defy-wireless's [versions and the manifest](https://github.com/borovaka/zmk-defy-wireless/blob/main/docs/versions.md).

`debug ports` reads the port list the operating system keeps: sysfs on Linux, the I/O Kit registry through `ioreg` on macOS, and the device manager on Windows. It opens no port and sends nothing to any device.

```
PORT                  USB ID     NEURON  PRODUCT  SERIAL            LOCATION
/dev/cu.usbmodem1101  1d50:615e  ZMK     Defy     0123456789ABCDEF  0x01100000
/dev/cu.usbmodem1104  1d50:615e  ZMK     Defy     0123456789ABCDEF  0x01100000
```

The Neuron shows up as one of:

| USB ID | NEURON | Running |
|---|---|---|
| `1d50:615e`, product `Defy` | ZMK | ZMK; it has two ports, Studio and the half updater |
| `35ef:0012` | Dygma firmware | Dygma's firmware |
| `35ef:0013` | bootloader | its bootloader |
| `2fe3:0100`, product `Defy UF2 loader` | UF2 loader | its UF2 loader, which shows the `DEFY` drive. The ID is Zephyr's testing ID for now |

If the Neuron shows a drive named `DEFY`, it is waiting for a UF2 file. Unplug it and plug it back in to start ZMK. If the drive comes back, copy the UF2 file again, or run `defy-installer update`. `update` restarts the Neuron through Dygma's bootloader, without the button, and updates ZMK as usual if ZMK starts. If the drive comes back instead, ZMK on the Neuron is incomplete or does not start. `update` then writes the Neuron again and keeps its Bluetooth pairings, then writes the halves if their images differ from the build's.

## Requirements

- macOS 13 or later, Windows 10 or later, or Linux.
- On Linux, access to `/dev/ttyACM*`; see [installing](#installing).
- A network connection for `install`, `update` and `uninstall`, unless you give `--firmware`.

## Building

Go 1.27 or later. There is no cgo, so any OS builds for every other:

```sh
go build .
GOOS=windows GOARCH=amd64 go build .
go install github.com/borovaka/defy-installer@latest
```

A `v*` tag runs `.github/workflows/release.yml`, which:

1. Runs the tests.
2. Builds six binaries from scratch with `packaging/build.sh`.
3. Writes `THIRD_PARTY_LICENSES`: the licences of Go's runtime and standard library and of every module linked into any of the binaries, with a module's `NOTICE` file after its licence and the full Apache License 2.0 text when a licence names it without its terms.
4. Checks that a binary reports the tag as its version.
5. Joins the two macOS binaries into one universal binary with `packaging/universal`, which writes the same file as Apple's `lipo -create`.
6. Packs the five archives with `packaging/archive`. Each holds the program, the settings template, `packaging/README.txt`, `LICENSE` and `THIRD_PARTY_LICENSES`, in a fixed file order with the commit's time, so that the same commit and Go version give the same archives.
7. Copies `packaging/install.sh` and `packaging/install.ps1` beside the archives, for the one-line installs.
8. Writes `SHA256SUMS` for the archives, `THIRD_PARTY_LICENSES` and the install scripts.
9. Records a build provenance attestation for each archive, on a public repository only.
10. Attaches them to a draft release, which the maintainer publishes by hand.

## Development

```sh
go test ./...
go vet ./...
go fix -diff ./...
```

The code lives under `internal/`:

| Package | What |
|---|---|
| `commands` | The command line: commands, flags and output |
| `firmware` | Reading and checking images: Intel HEX, ZMK and Dygma Neuron images, the halves' packages |
| `firmware/firmwaretest` | Building images for tests. Its half packages match zmk-defy-wireless's `package_factory.py` byte for byte |
| `flow` | The commands that reach the keyboard: `install`, `update`, `uninstall` and `status`, and the writes they are built from, with the order of ports, checks, writes and waits |
| `focus` | Dygma's Neuron firmware over Focus: asking it for its bootloader, and writing Dygma's keyscanner back to the halves |
| `neuron` | The Neuron: recognising it by its USB IDs, entering its bootloader from ZMK and waiting for the firmware to come back, and writing it through Dygma's bootloader |
| `release` | Finding the firmware to install: a release on GitHub through its download links, a zip or folder on this computer, or Dygma's 2.3.0 files. It reads `manifest.json` and checks each image against it |
| `settings` | The settings file, `defy-installer.yaml`: finding it, checking it, and changing one value while every other byte stays. The file the installer creates comes from `internal/settings/defy-installer.yaml` |
| `updater` | The ZMK Neuron's half updater: its frames and sessions, finding its port, writing both halves, and the version, status and Studio requests |
| `updater/updatertest` | A fake of the half updater's port for tests, with a model of both halves, which the `updater` and `flow` tests share |
| `usbserial` | Listing USB serial ports on each OS, and opening them with [go.bug.st/serial](https://github.com/bugst/go-serial) |
| `wiretest` | Replaying recorded serial exchanges in tests |

`packaging/` holds what a release uses: `build.sh`, which builds the six binaries, `universal`, which writes the universal macOS binary, `archive`, which packs the archives, `README.txt`, which goes into each archive, and `install.sh` and `install.ps1`, the one-line installs. `packaging/install` tests the scripts on each OS against a fake release served over HTTP.

The `testdata/*.json` files in `flow` were recorded from an earlier Python version of this installer, which is not published, running against its tests' fake devices: whole command runs with every port they opened. Each recorded scenario's `argv` is that version's command line, such as `halves-flash` or `--preserve-settings`, not this one's. The tests make the Go code open the same ports in the same order and send the same bytes on each.

That version never sent PAIRING, so one exchange in `flow`'s `halves-write.json` and `halves-probe.json` was added by hand: the PAIRING request, and the refusal that Neuron firmware from before the pairing readout sends, status -128, `ENOTCONN`, with no payload. Each file's comment gives the bytes. The rest of each file is as recorded. Firmware that answers PAIRING, and the version requests, have no recordings; the fake in `updater/updatertest` stands in for them.

Each OS has its own small file that collects the raw data: `list_linux.go`, `list_darwin.go` and `list_windows.go`. The parsing lives in files without build tags, so its tests run everywhere. They use fixtures: a sysfs tree in `sysfs_test.go`, and trimmed `ioreg` output in `internal/usbserial/testdata/` with the serial numbers replaced.

## Licence

MIT. See [LICENSE](LICENSE). The release archives also contain Go's runtime and standard library and other Go modules, under their own licences; `THIRD_PARTY_LICENSES` holds their texts.
