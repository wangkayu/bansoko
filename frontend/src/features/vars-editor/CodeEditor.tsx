import { forwardRef, useEffect, useImperativeHandle, useRef } from "react";
import { Compartment, EditorState } from "@codemirror/state";
import { EditorView, drawSelection, highlightActiveLine, highlightSpecialChars, keymap, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, bracketMatching, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import { javascript } from "@codemirror/lang-javascript";
import { lintGutter, setDiagnostics } from "@codemirror/lint";
import { tags } from "@lezer/highlight";
import type { EditorMarker } from "./varsEditor";

// The variables program's code editor (CodeMirror 6). The whole module loads only via React.lazy
//   and never enters the main bundle; colors come from the app's HSL tokens, following the light
//   and dark themes automatically.

export type CodeEditorHandle = {
  /** Inserts text at the caret (or over the selection) and keeps focus. */
  insert: (text: string) => void;
};

const highlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.controlKeyword, tags.definitionKeyword, tags.moduleKeyword], color: "hsl(var(--primary))", fontWeight: "600" },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: "hsl(var(--status-online))" },
  { tag: [tags.number, tags.bool, tags.null], color: "hsl(var(--status-warning))" },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: "hsl(var(--muted-foreground))", fontStyle: "italic" },
  { tag: [tags.propertyName, tags.function(tags.variableName)], color: "hsl(var(--accent-foreground))" },
  { tag: tags.invalid, color: "hsl(var(--destructive))" },
]);

const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "13px", backgroundColor: "hsl(var(--muted))", color: "hsl(var(--foreground))" },
  "&.cm-focused": { outline: "2px solid hsl(var(--ring))", outlineOffset: "1px" },
  ".cm-scroller": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", lineHeight: "1.55" },
  ".cm-content": { caretColor: "hsl(var(--foreground))", padding: "8px 0" },
  ".cm-cursor": { borderLeftColor: "hsl(var(--foreground))" },
  ".cm-gutters": { backgroundColor: "hsl(var(--muted))", color: "hsl(var(--muted-foreground))", border: "none" },
  ".cm-activeLine": { backgroundColor: "hsl(var(--accent) / 0.35)" },
  ".cm-activeLineGutter": { backgroundColor: "transparent", color: "hsl(var(--foreground))" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "hsl(var(--primary) / 0.25)" },
  ".cm-tooltip": { backgroundColor: "hsl(var(--popover))", color: "hsl(var(--popover-foreground))", border: "1px solid hsl(var(--border))" },
});

type Props = {
  value: string;
  onChange?: (value: string) => void;
  markers?: readonly EditorMarker[];
  readOnly?: boolean;
  label: string;
};

const CodeEditor = forwardRef<CodeEditorHandle, Props>(function CodeEditor({ value, onChange, markers = [], readOnly = false, label }, ref) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const onChangeRef = useRef(onChange);
  const access = useRef(new Compartment());
  const latest = useRef({ value, markers, readOnly });
  latest.current = { value, markers, readOnly };
  onChangeRef.current = onChange;

  useImperativeHandle(ref, () => ({
    insert(text: string) {
      const current = view.current;
      if (!current || latest.current.readOnly) return;
      const selection = current.state.selection.main;
      current.dispatch({
        changes: { from: selection.from, to: selection.to, insert: text },
        selection: { anchor: selection.from + text.length },
        scrollIntoView: true,
      });
      current.focus();
    },
  }));

  useEffect(() => {
    if (!host.current) return undefined;
    const editor = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: latest.current.value,
        extensions: [
          lineNumbers(),
          highlightSpecialChars(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          highlightActiveLine(),
          lintGutter(),
          javascript(),
          syntaxHighlighting(highlight),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ "aria-label": label, spellcheck: "false" }),
          theme,
          access.current.of([EditorState.readOnly.of(latest.current.readOnly), EditorView.editable.of(!latest.current.readOnly)]),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) onChangeRef.current?.(update.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = editor;
    return () => {
      editor.destroy();
      view.current = null;
    };
    // The editor is created once; later props are applied by the effects below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const editor = view.current;
    if (editor && editor.state.doc.toString() !== value) {
      editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: value } });
    }
  }, [value]);

  useEffect(() => {
    view.current?.dispatch({
      effects: access.current.reconfigure([EditorState.readOnly.of(readOnly), EditorView.editable.of(!readOnly)]),
    });
  }, [readOnly]);

  useEffect(() => {
    const editor = view.current;
    if (!editor) return;
    const length = editor.state.doc.length;
    editor.dispatch(
      setDiagnostics(
        editor.state,
        markers
          .filter((marker) => marker.from < length || length === 0)
          .map((marker) => ({
            from: Math.min(marker.from, length),
            to: Math.min(Math.max(marker.to, marker.from), length),
            severity: marker.severity,
            message: marker.message,
          })),
      ),
    );
  }, [markers]);

  return <div ref={host} className="tw:h-full tw:min-h-0 tw:overflow-hidden tw:rounded-control tw:border tw:border-border" data-testid="vars-code-editor" />;
});

export default CodeEditor;
