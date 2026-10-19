// What every applet can use, in addition to the ECMAScript standard library
// and the declarations of the caller.

declare module "go-applets" {
  /**
   * An applet receives an iterable of input objects and its params, and
   * returns an iterable of output objects, usually from a generator function.
   * Reading the next input object waits for the host; the loop over the input
   * ends when the host has sent all objects. Code after the loop is the place
   * to output objects that depend on all input.
   */
  export type Applet<In extends object, Out extends object = In, Params = undefined> = (
    input: Iterable<In>,
    params: Params,
  ) => Iterable<Out>;
}

/** Logs a line, which the host receives. */
declare var console: {
  log(...data: unknown[]): void;
  info(...data: unknown[]): void;
  warn(...data: unknown[]): void;
  error(...data: unknown[]): void;
  debug(...data: unknown[]): void;
};
