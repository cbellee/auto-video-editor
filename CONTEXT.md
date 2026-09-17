# Automatic Video Editing

This context describes how source footage is evaluated and assembled into a finished video while preserving the editor's chosen intent.

## Language

**Source Clip**:
A video file supplied as material for an editing run.
_Avoid_: Input file, raw

**Candidate Segment**:
A continuous portion of a Source Clip considered for inclusion in the finished video.
_Avoid_: Snippet, chunk

**Technical Quality**:
The degree to which a Candidate Segment is visually usable, considering defects such as blur, shake, and poor exposure.
_Avoid_: Best looking, good footage

**Visual Interest**:
The degree to which a technically usable Candidate Segment contributes engaging subject matter or useful variety.
_Avoid_: Quality, importance

**Visual Redundancy**:
The degree to which Candidate Segments repeat the same subject, action, viewpoint, or narrative contribution.
_Avoid_: Duplicate file, similar clip

**Quality Profile**:
A named level of tolerance for technical defects when deciding whether a Candidate Segment is usable.
_Avoid_: Preset, sensitivity

**Edit Intent**:
The user-selected organizing goal that determines how Candidate Segments are ordered and evaluated for coherence.
_Avoid_: Mode, style

**Chronological Intent**:
An Edit Intent that preserves the capture order of Source Clips while selecting their strongest Candidate Segments.
_Avoid_: Timeline mode, date sort

**Thematic Intent**:
An Edit Intent that may reorder Candidate Segments to group related subjects, activities, or visual motifs.
_Avoid_: AI mode, creative mode

**Theme**:
An optional user-provided subject or idea that guides selection and ordering under Thematic Intent.
_Avoid_: Prompt, topic string

**Selection Guidance**:
Optional user direction about subjects or qualities to prefer or avoid when choosing Candidate Segments.
_Avoid_: Prompt, instructions

**Selected Segment**:
A Candidate Segment accepted for inclusion in an Edit Plan.
_Avoid_: Chosen clip, keeper

**Edit Plan**:
The reusable description of selected Candidate Segments, their order, timing, transitions, and audio treatment.
_Avoid_: Storyboard, timeline JSON

**Transition**:
The visual and audible treatment used to move from one Selected Segment to the next.
_Avoid_: Effect, animation

**Dialogue Continuity**:
An intelligible spoken passage that may span the visual boundary between adjacent Selected Segments.
_Avoid_: Audio overlap, voice carry

**Dialogue Segment**:
A Candidate Segment whose source audio contains speech intended to remain intelligible in the Finished Video.
_Avoid_: Talking clip, voice clip

**Music Track**:
An optional audio recording used as the Finished Video's musical bed and timing reference.
_Avoid_: Song, backing track

**Music Cue**:
A beat, phrase boundary, or section boundary that provides a preferred point for an edit.
_Avoid_: Beat marker, sync point

**Finished Video**:
The single rendered video produced from an Edit Plan.
_Avoid_: Output file, final cut
