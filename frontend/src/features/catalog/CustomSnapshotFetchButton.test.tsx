import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { CustomSnapshotFetchButton } from "./CustomSnapshotFetchButton";
afterEach(cleanup);
it("fetches only on click and emits the exact identity", async () => {
 const request={candidate_id:"candidate",revision:3,expected_version:7};const identity={snapshot_id:"snapshot",snapshot_version:2,content_hash:`sha256:${"a".repeat(64)}`};const fetchSnapshot=vi.fn().mockResolvedValue(identity);const onSnapshot=vi.fn();
 render(<CustomSnapshotFetchButton request={request} fetchSnapshot={fetchSnapshot} onSnapshot={onSnapshot}/>);
 expect(fetchSnapshot).not.toHaveBeenCalled();fireEvent.click(screen.getByRole("button"));await waitFor(()=>expect(onSnapshot).toHaveBeenCalledWith(identity));expect(fetchSnapshot).toHaveBeenCalledTimes(1);expect(fetchSnapshot).toHaveBeenCalledWith(request);
});
it("exposes failure without inventing a snapshot", async () => {
 const fetchSnapshot=vi.fn().mockRejectedValue(new Error("bad feed"));const onSnapshot=vi.fn();render(<CustomSnapshotFetchButton request={{candidate_id:"candidate",revision:1,expected_version:1}} fetchSnapshot={fetchSnapshot} onSnapshot={onSnapshot}/>);
 fireEvent.click(screen.getByRole("button"));await screen.findByRole("alert");expect(onSnapshot).not.toHaveBeenCalled();expect(fetchSnapshot).toHaveBeenCalledTimes(1);
});
