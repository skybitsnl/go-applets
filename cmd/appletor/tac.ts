import type { Applet } from "go-applets";
import type { File } from "file";

const applet: Applet<File> = function* (files) {
	for (const file of files) {
		const hadNewline = file.contents.endsWith("\n");
		const lines = file.contents.split("\n");
		if (hadNewline) {
			lines.pop();
			lines.unshift("");
		}
		yield {
			name: file.name,
			contents: lines.reverse().join("\n"),
		};
	}
}
export default applet;
