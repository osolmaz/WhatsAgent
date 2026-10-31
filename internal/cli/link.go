package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/paths"
	"github.com/osolmaz/WhatsAgent/internal/wa"
)

// linkFlags holds the flags of `whatsagent link`.
type linkFlags struct {
	phone      string
	qrFormat   string
	importFrom string
}

func newLink(env paths.Env, log *slog.Logger) *cobra.Command {
	var flags linkFlags
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Link WhatsAgent to a WhatsApp account",
		Long: "Link WhatsAgent to a WhatsApp account as a linked device. By default it prints a QR code\n" +
			"to scan in WhatsApp under Linked devices. --phone pairs with a code instead, and --import\n" +
			"copies the login from an existing whatsmeow session store.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := paths.Resolve(env)
			if err != nil {
				return err
			}
			if flags.importFrom != "" {
				return linkImport(cmd.Context(), cmd.OutOrStdout(), dirs, flags.importFrom, log)
			}
			show, err := qrPrinter(cmd.OutOrStdout(), flags.qrFormat)
			if err != nil {
				return err
			}
			return linkNew(cmd.Context(), cmd.OutOrStdout(), dirs, flags.phone, show, log)
		},
	}
	cmd.Flags().StringVar(&flags.phone, "phone", "", "pair with a code for this phone number, in international format, instead of a QR code")
	cmd.Flags().StringVar(&flags.qrFormat, "qr-format", "terminal", "how to print the QR code: terminal or text")
	cmd.Flags().StringVar(&flags.importFrom, "import", "", "copy the login from this whatsmeow session store file instead of pairing")
	return cmd
}

// qrPrinter returns the function that prints each QR code in the chosen format.
func qrPrinter(out io.Writer, format string) (func(string), error) {
	switch format {
	case "terminal":
		return func(code string) {
			fmt.Fprintln(out, "Scan this code in WhatsApp under Settings > Linked devices:")
			qrterminal.GenerateHalfBlock(code, qrterminal.M, out)
		}, nil
	case "text":
		return func(code string) { fmt.Fprintln(out, code) }, nil
	default:
		return nil, fmt.Errorf("unknown --qr-format %q; use terminal or text", format)
	}
}

// linkNew pairs a new session store by QR code or phone-number code.
func linkNew(ctx context.Context, out io.Writer, dirs paths.Dirs, phone string, showQR func(string), log *slog.Logger) error {
	client, err := wa.Open(ctx, dirs.SessionDB(), log)
	if err != nil {
		return err
	}
	defer client.Close()
	opts := wa.LinkOptions{
		Phone: phone,
		OnQR:  showQR,
		OnPairCode: func(code string) {
			fmt.Fprintf(out, "On the phone, open WhatsApp > Linked devices > Link with phone number, and enter: %s\n", code)
		},
	}
	if err := client.Link(ctx, opts); err != nil {
		return err
	}
	fmt.Fprintf(out, "Linked as %s.\n", client.OwnJID())
	return nil
}

// linkImport copies an existing session store into WhatsAgent's session store and checks it.
func linkImport(ctx context.Context, out io.Writer, dirs paths.Dirs, src string, log *slog.Logger) error {
	if err := wa.ImportStore(src, dirs.SessionDB()); err != nil {
		return err
	}
	client, err := wa.Open(ctx, dirs.SessionDB(), log)
	if err != nil {
		return fmt.Errorf("open the imported session store: %w", err)
	}
	defer client.Close()
	if !client.Linked() {
		return errors.New("the imported session store holds no linked device")
	}
	fmt.Fprintf(out, "Linked as %s, with the login imported from %s.\n", client.OwnJID(), src)
	fmt.Fprintln(out, "Stop the program that used that store before starting WhatsAgent: two programs on one linked device disconnect each other.")
	return nil
}
