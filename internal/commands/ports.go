// SPDX-License-Identifier: MIT

package commands

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/borovaka/defy-installer/internal/neuron"
	"github.com/borovaka/defy-installer/internal/usbserial"
)

func (d Deps) portsCommand() *cli.Command {
	return &cli.Command{
		Name:  "ports",
		Usage: "list the USB serial ports and which are the Neuron's",
		Description: "Reads the list the operating system keeps. " +
			"It opens no port and sends nothing to any device.",
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "json", Usage: "print JSON"},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			ports, err := d.Env.List(ctx)
			if err != nil {
				return err
			}
			if cmd.Bool("json") {
				return writePortsJSON(cmd.Root().Writer, ports)
			}
			return writePorts(cmd.Root().Writer, ports)
		},
	}
}

func writePorts(w io.Writer, ports []usbserial.Port) error {
	if len(ports) == 0 {
		_, err := fmt.Fprintln(w, "No USB serial ports.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PORT\tUSB ID\tNEURON\tPRODUCT\tSERIAL\tLOCATION")
	for _, p := range ports {
		mode := neuron.ModeOf(p)
		what := mode.String()
		if mode == neuron.NotNeuron {
			what = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", p.Path, p.ID, what, dash(p.Product), dash(p.Serial), dash(p.Location))
	}
	return tw.Flush()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

type portJSON struct {
	Path     string `json:"path"`
	USBID    string `json:"usb_id"`
	Neuron   string `json:"neuron,omitzero"`
	Product  string `json:"product,omitzero"`
	Serial   string `json:"serial,omitzero"`
	Location string `json:"location,omitzero"`
}

func writePortsJSON(w io.Writer, ports []usbserial.Port) error {
	out := make([]portJSON, 0, len(ports))
	for _, p := range ports {
		entry := portJSON{Path: p.Path, USBID: p.ID.String(), Product: p.Product, Serial: p.Serial, Location: p.Location}
		if mode := neuron.ModeOf(p); mode != neuron.NotNeuron {
			entry.Neuron = mode.String()
		}
		out = append(out, entry)
	}
	if err := json.MarshalWrite(w, out, jsontext.WithIndent("  ")); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}
