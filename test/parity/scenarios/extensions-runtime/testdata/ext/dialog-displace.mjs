// Pi bdd9f039 interactive-mode.ts pushExtensionDialog (#11166): a dialog that opens while another is shown hides it without settling it; closing the
// shown dialog shows the earlier one again. The command opens a selector without waiting for it, then an input, answers the input, and the selector
// comes back and settles with its own answer.
export default function (pi) {
  pi.registerCommand("dialog-displace", {
    description: "Open a selector, then an input that stacks over it",
    handler: async (_args, ctx) => {
      const earlier = ctx.ui.select("Displaced selector", ["one", "two"]).then(
        (value) => ctx.ui.notify(`displaced-resolved:${value}`, "info"),
        (error) => ctx.ui.notify(`displaced-rejected:${error.message}`, "error"),
      );
      try {
        const value = await ctx.ui.input("Displacing input", "type here");
        ctx.ui.notify(`displacing-result:${value}`, "info");
      } catch (error) {
        ctx.ui.notify(`displacing-rejected:${error.message}`, "error");
      }
      await earlier;
    },
  });
}
