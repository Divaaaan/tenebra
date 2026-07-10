# Third-Party Notices

Tenebra is distributed under the GNU General Public License, version 3; the
full text of that license is in the [LICENSE](LICENSE) file at the repository
root. In addition, Tenebra redistributes, links against, or compiles in the
third-party software listed below. This file aggregates the copyright notices,
license identifiers, and attributions those components require. It is generated
by `scripts/generate-notices.mjs` from the project manifests; edit that script
rather than this file.

Contents:

1. Bundled runtime components
2. Go core dependencies
3. Rust (Tauri) dependencies
4. Frontend (npm) dependencies
5. Bundled fonts
6. Full license texts

## 1. Bundled runtime components

These files are produced by `scripts/fetch-resources.ps1` / `.sh` and shipped
inside the installer (declared under `bundle.resources` in
`ui-desktop/src-tauri/tauri.conf.json`). They are redistributed unmodified.

### Wintun

- Component: `wintun.dll` (Wintun 0.14.1)
- License: Wintun Prebuilt Binaries License (a separate license from WireGuard
  LLC — the prebuilt DLL is **not** distributed under the GPLv2 that covers the
  Wintun source)
- Copyright: WireGuard LLC
- Attribution: This is the official prebuilt build downloaded from
  <https://www.wintun.net/builds>, bundled without modification. Tenebra does
  not call Wintun directly; sing-box loads it through the documented Permitted
  API declared in `wintun.h`. The DLL is redistributed alongside software that
  uses it only via that Permitted API, which this license allows. Full text:

```text
Prebuilt Binaries License
-------------------------

1. DEFINITIONS. "Software" means the precise contents of the "wintun.dll"
   files that are included in the .zip file that contains this document as
   downloaded from wintun.net/builds.

2. LICENSE GRANT. WireGuard LLC grants to you a non-exclusive and
   non-transferable right to use Software for lawful purposes under certain
   obligations and limited rights as set forth in this agreement.

3. RESTRICTIONS. Software is owned and copyrighted by WireGuard LLC. It is
   licensed, not sold. Title to Software and all associated intellectual
   property rights are retained by WireGuard. You must not:
   a. reverse engineer, decompile, disassemble, extract from, or otherwise
      modify the Software;
   b. modify or create derivative work based upon Software in whole or in
      parts, except insofar as only the API interfaces of the "wintun.h" file
      distributed alongside the Software (the "Permitted API") are used;
   c. remove any proprietary notices, labels, or copyrights from the Software;
   d. resell, redistribute, lease, rent, transfer, sublicense, or otherwise
      transfer rights of the Software without the prior written consent of
      WireGuard LLC, except insofar as the Software is distributed alongside
      other software that uses the Software only via the Permitted API;
   e. use the name of WireGuard LLC, the WireGuard project, the Wintun
      project, or the names of its contributors to endorse or promote products
      derived from the Software without specific prior written consent.

4. LIMITED WARRANTY. THE SOFTWARE IS PROVIDED "AS IS" AND WITHOUT WARRANTY OF
   ANY KIND. WIREGUARD LLC HEREBY EXCLUDES AND DISCLAIMS ALL IMPLIED OR
   STATUTORY WARRANTIES, INCLUDING ANY WARRANTIES OF MERCHANTABILITY, FITNESS
   FOR A PARTICULAR PURPOSE, QUALITY, NON-INFRINGEMENT, TITLE, RESULTS,
   EFFORTS, OR QUIET ENJOYMENT. THERE IS NO WARRANTY THAT THE PRODUCT WILL BE
   ERROR-FREE OR WILL FUNCTION WITHOUT INTERRUPTION. YOU ASSUME THE ENTIRE
   RISK FOR THE RESULTS OBTAINED USING THE PRODUCT. TO THE EXTENT THAT
   WIREGUARD LLC MAY NOT DISCLAIM ANY WARRANTY AS A MATTER OF APPLICABLE LAW,
   THE SCOPE AND DURATION OF SUCH WARRANTY WILL BE THE MINIMUM PERMITTED UNDER
   SUCH LAW. ALL EXPRESS OR IMPLIED CONDITIONS, REPRESENTATIONS AND
   WARRANTIES, INCLUDING ANY IMPLIED WARRANTY OF MERCHANTABILITY, FITNESS FOR
   A PARTICULAR PURPOSE OR NON-INFRINGEMENT ARE DISCLAIMED, EXCEPT TO THE
   EXTENT THAT THESE DISCLAIMERS ARE HELD TO BE LEGALLY INVALID.

5. LIMITATION OF LIABILITY. To the extent not prohibited by law, in no event
   WireGuard LLC or any third-party-developer will be liable for any lost
   revenue, profit or data or for special, indirect, consequential, incidental
   or punitive damages, however caused regardless of the theory of liability,
   arising out of or related to the use of or inability to use Software, even
   if WireGuard LLC has been advised of the possibility of such damages.
   Solely you are responsible for determining the appropriateness of using
   Software and accept full responsibility for all risks associated with its
   exercise of rights under this agreement, including but not limited to the
   risks and costs of program errors, compliance with applicable laws, damage
   to or loss of data, programs or equipment, and unavailability or
   interruption of operations. The foregoing limitations will apply even if
   the above stated warranty fails of its essential purpose. You acknowledge,
   that it is in the nature of software that software is complex and not
   completely free of errors. In no event shall WireGuard LLC or any
   third-party-developer be liable to you under any theory for any damages
   suffered by you or any user of Software or for any special, incidental,
   indirect, consequential or similar damages (including without limitation
   damages for loss of business profits, business interruption, loss of
   business information or any other pecuniary loss) arising out of the use or
   inability to use Software, even if WireGuard LLC has been advised of the
   possibility of such damages and regardless of the legal or quitable theory
   (contract, tort, or otherwise) upon which the claim is based.

6. TERMINATION. This agreement is affected until terminated. You may
   terminate this agreement at any time. This agreement will terminate
   immediately without notice from WireGuard LLC if you fail to comply with
   the terms and conditions of this agreement. Upon termination, you must
   delete Software and all copies of Software and cease all forms of
   distribution of Software.

7. SEVERABILITY. If any provision of this agreement is held to be
   unenforceable, this agreement will remain in effect with the provision
   omitted, unless omission would frustrate the intent of the parties, in
   which case this agreement will immediately terminate.

8. RESERVATION OF RIGHTS. All rights not expressly granted in this agreement
   are reserved by WireGuard LLC. For example, WireGuard LLC reserves the
   right at any time to cease development of Software, to alter distribution
   details, features, specifications, capabilities, functions, licensing
   terms, release dates, APIs, ABIs, general availability, or other
   characteristics of the Software.
```

### sing-box

- Component: `sing-box.exe` (sing-box 1.13.13, official windows-amd64 release)
- License: GPL-3.0-or-later, with an additional term under section 7 of the GPL
- Copyright: Copyright (C) 2022 by nekohasekai <contact-sagernet@sekai.icu>
- Source: <https://github.com/SagerNet/sing-box>
- Attribution: The binary is bundled unmodified and invoked as a separate
  process. The full GPLv3 text is not duplicated here — it is the same license
  Tenebra ships in [LICENSE](LICENSE). The sing-box license adds the following
  term under GPLv3 section 7, quoted verbatim:

  > In addition, no derivative work may use the name or imply association
  > with this application without prior consent.

### GeoIP rule-set

- Component: `geoip-ru.srs`
- Source: <https://github.com/SagerNet/sing-geoip> (`rule-set` branch,
  `geoip-ru.srs`)
- License: GPL-3.0-or-later — Copyright (C) 2022 by nekohasekai
  <contact-sagernet@sekai.icu>
- Data attribution: The rule-set is compiled from the MaxMind GeoLite2 Country
  database (obtained via <https://github.com/Dreamacro/maxmind-geoip>). MaxMind
  requires the following attribution:

  > This product includes GeoLite2 Data created by MaxMind, available from
  > <https://www.maxmind.com>.

### GeoSite rule-set

- Component: `geosite-ru.srs`
- Source: <https://github.com/SagerNet/sing-geosite> (`rule-set` branch,
  `geosite-category-ru.srs`)
- License: GPL-3.0-or-later — Copyright (C) 2022 by nekohasekai
  <contact-sagernet@sekai.icu>
- Data attribution: The rule-set is compiled from the v2fly community domain
  list, <https://github.com/v2fly/domain-list-community>, which is distributed
  under the MIT License — Copyright (c) 2018-2019 V2Ray. The MIT License text
  appears in section 6.

## 2. Go core dependencies

Direct dependencies from `go.mod`. The module graph contains no other
dependencies (`go.sum` lists only these two modules).

- **github.com/Microsoft/go-winio** `v0.6.2` — MIT — Copyright (c) 2015 Microsoft
- **golang.org/x/sys** `v0.41.0` — BSD-3-Clause — Copyright 2009 The Go Authors

## 3. Rust (Tauri) dependencies

Direct dependencies from `ui-desktop/src-tauri/Cargo.toml` (including the build
and Windows-target dependencies). Each is dual-licensed; a distributor may use
either of the listed licenses.

- **tauri-build** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-dialog** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-shell** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-autostart** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-single-instance** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-deep-link** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-notification** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-updater** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **tauri-plugin-process** `2` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **serde** `1` — MIT OR Apache-2.0 — The Serde developers
- **serde_json** `1` — MIT OR Apache-2.0 — The Serde developers
- **url** `2` — MIT OR Apache-2.0 — Copyright (c) 2013-2025 The rust-url developers
- **windows-sys** `0.61` — MIT OR Apache-2.0 — Copyright (c) Microsoft Corporation

## 4. Frontend (npm) dependencies

Direct production dependencies from `ui-desktop/package.json`. `devDependencies`
are build-time only and are not redistributed in the application, so they are
not listed here.

- **@fontsource-variable/jetbrains-mono** `5.2.8` — OFL-1.1 — Copyright 2020 The JetBrains Mono Project Authors (https://github.com/JetBrains/JetBrainsMono) — npm package MIT-licensed; the bundled font files are under the SIL Open Font License 1.1.
- **@fontsource-variable/space-grotesk** `5.2.10` — OFL-1.1 — Copyright 2020 The Space Grotesk Project Authors (https://github.com/floriankarsten/space-grotesk) — npm package MIT-licensed; the bundled font files are under the SIL Open Font License 1.1.
- **@tauri-apps/api** `2.11.1` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **@tauri-apps/plugin-autostart** `2.5.0` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **@tauri-apps/plugin-dialog** `2.7.1` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **@tauri-apps/plugin-process** `2.3.1` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **@tauri-apps/plugin-shell** `2.3.5` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **@tauri-apps/plugin-updater** `2.10.1` — Apache-2.0 OR MIT — Copyright (c) 2017 - Present Tauri Apps Contributors
- **react** `18.3.1` — MIT — Copyright (c) Facebook, Inc. and its affiliates
- **react-dom** `18.3.1` — MIT — Copyright (c) Facebook, Inc. and its affiliates

## 5. Bundled fonts

The application embeds the following variable fonts (delivered as `.woff2`
assets compiled into the frontend bundle). Both are licensed under the SIL Open
Font License, Version 1.1, whose full text appears in section 6.

- **JetBrains Mono** — SIL OFL 1.1 — Copyright 2020 The JetBrains Mono Project
  Authors (<https://github.com/JetBrains/JetBrainsMono>)
- **Space Grotesk** — SIL OFL 1.1 — Copyright 2020 The Space Grotesk Project
  Authors (<https://github.com/floriankarsten/space-grotesk>)

## 6. Full license texts

The copyright holder that applies to each package is listed in its entry above.
The following are the full texts of the licenses referenced in this file. The
GPL-3.0 text that covers Tenebra and the bundled sing-box components is in
[LICENSE](LICENSE) and is not repeated here.

### MIT License

Applies to: github.com/Microsoft/go-winio, tauri-build, tauri, tauri-plugin-dialog, tauri-plugin-shell, tauri-plugin-autostart, tauri-plugin-single-instance, tauri-plugin-deep-link, tauri-plugin-notification, tauri-plugin-updater, tauri-plugin-process, serde, serde_json, url, windows-sys, @tauri-apps/api, @tauri-apps/plugin-autostart, @tauri-apps/plugin-dialog, @tauri-apps/plugin-process, @tauri-apps/plugin-shell, @tauri-apps/plugin-updater, react, react-dom.

```text
MIT License

Copyright (c) the respective authors (see the component entries above for the
copyright holder that applies to each package)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

### BSD 3-Clause License

Applies to: golang.org/x/sys.

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

### Apache License 2.0

Applies to: tauri-build, tauri, tauri-plugin-dialog, tauri-plugin-shell, tauri-plugin-autostart, tauri-plugin-single-instance, tauri-plugin-deep-link, tauri-plugin-notification, tauri-plugin-updater, tauri-plugin-process, serde, serde_json, url, windows-sys, @tauri-apps/api, @tauri-apps/plugin-autostart, @tauri-apps/plugin-dialog, @tauri-apps/plugin-process, @tauri-apps/plugin-shell, @tauri-apps/plugin-updater.

```text
                                 Apache License
                           Version 2.0, January 2004
                        http://www.apache.org/licenses/

   TERMS AND CONDITIONS FOR USE, REPRODUCTION, AND DISTRIBUTION

   1. Definitions.

      "License" shall mean the terms and conditions for use, reproduction,
      and distribution as defined by Sections 1 through 9 of this document.

      "Licensor" shall mean the copyright owner or entity authorized by
      the copyright owner that is granting the License.

      "Legal Entity" shall mean the union of the acting entity and all
      other entities that control, are controlled by, or are under common
      control with that entity. For the purposes of this definition,
      "control" means (i) the power, direct or indirect, to cause the
      direction or management of such entity, whether by contract or
      otherwise, or (ii) ownership of fifty percent (50%) or more of the
      outstanding shares, or (iii) beneficial ownership of such entity.

      "You" (or "Your") shall mean an individual or Legal Entity
      exercising permissions granted by this License.

      "Source" form shall mean the preferred form for making modifications,
      including but not limited to software source code, documentation
      source, and configuration files.

      "Object" form shall mean any form resulting from mechanical
      transformation or translation of a Source form, including but
      not limited to compiled object code, generated documentation,
      and conversions to other media types.

      "Work" shall mean the work of authorship, whether in Source or
      Object form, made available under the License, as indicated by a
      copyright notice that is included in or attached to the work
      (an example is provided in the Appendix below).

      "Derivative Works" shall mean any work, whether in Source or Object
      form, that is based on (or derived from) the Work and for which the
      editorial revisions, annotations, elaborations, or other modifications
      represent, as a whole, an original work of authorship. For the purposes
      of this License, Derivative Works shall not include works that remain
      separable from, or merely link (or bind by name) to the interfaces of,
      the Work and Derivative Works thereof.

      "Contribution" shall mean any work of authorship, including
      the original version of the Work and any modifications or additions
      to that Work or Derivative Works thereof, that is intentionally
      submitted to Licensor for inclusion in the Work by the copyright owner
      or by an individual or Legal Entity authorized to submit on behalf of
      the copyright owner. For the purposes of this definition, "submitted"
      means any form of electronic, verbal, or written communication sent
      to the Licensor or its representatives, including but not limited to
      communication on electronic mailing lists, source code control systems,
      and issue tracking systems that are managed by, or on behalf of, the
      Licensor for the purpose of discussing and improving the Work, but
      excluding communication that is conspicuously marked or otherwise
      designated in writing by the copyright owner as "Not a Contribution."

      "Contributor" shall mean Licensor and any individual or Legal Entity
      on behalf of whom a Contribution has been received by Licensor and
      subsequently incorporated within the Work.

   2. Grant of Copyright License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      copyright license to reproduce, prepare Derivative Works of,
      publicly display, publicly perform, sublicense, and distribute the
      Work and such Derivative Works in Source or Object form.

   3. Grant of Patent License. Subject to the terms and conditions of
      this License, each Contributor hereby grants to You a perpetual,
      worldwide, non-exclusive, no-charge, royalty-free, irrevocable
      (except as stated in this section) patent license to make, have made,
      use, offer to sell, sell, import, and otherwise transfer the Work,
      where such license applies only to those patent claims licensable
      by such Contributor that are necessarily infringed by their
      Contribution(s) alone or by combination of their Contribution(s)
      with the Work to which such Contribution(s) was submitted. If You
      institute patent litigation against any entity (including a
      cross-claim or counterclaim in a lawsuit) alleging that the Work
      or a Contribution incorporated within the Work constitutes direct
      or contributory patent infringement, then any patent licenses
      granted to You under this License for that Work shall terminate
      as of the date such litigation is filed.

   4. Redistribution. You may reproduce and distribute copies of the
      Work or Derivative Works thereof in any medium, with or without
      modifications, and in Source or Object form, provided that You
      meet the following conditions:

      (a) You must give any other recipients of the Work or
          Derivative Works a copy of this License; and

      (b) You must cause any modified files to carry prominent notices
          stating that You changed the files; and

      (c) You must retain, in the Source form of any Derivative Works
          that You distribute, all copyright, patent, trademark, and
          attribution notices from the Source form of the Work,
          excluding those notices that do not pertain to any part of
          the Derivative Works; and

      (d) If the Work includes a "NOTICE" text file as part of its
          distribution, then any Derivative Works that You distribute must
          include a readable copy of the attribution notices contained
          within such NOTICE file, excluding those notices that do not
          pertain to any part of the Derivative Works, in at least one
          of the following places: within a NOTICE text file distributed
          as part of the Derivative Works; within the Source form or
          documentation, if provided along with the Derivative Works; or,
          within a display generated by the Derivative Works, if and
          wherever such third-party notices normally appear. The contents
          of the NOTICE file are for informational purposes only and
          do not modify the License. You may add Your own attribution
          notices within Derivative Works that You distribute, alongside
          or as an addendum to the NOTICE text from the Work, provided
          that such additional attribution notices cannot be construed
          as modifying the License.

      You may add Your own copyright statement to Your modifications and
      may provide additional or different license terms and conditions
      for use, reproduction, or distribution of Your modifications, or
      for any such Derivative Works as a whole, provided Your use,
      reproduction, and distribution of the Work otherwise complies with
      the conditions stated in this License.

   5. Submission of Contributions. Unless You explicitly state otherwise,
      any Contribution intentionally submitted for inclusion in the Work
      by You to the Licensor shall be under the terms and conditions of
      this License, without any additional terms or conditions.
      Notwithstanding the above, nothing herein shall supersede or modify
      the terms of any separate license agreement you may have executed
      with Licensor regarding such Contributions.

   6. Trademarks. This License does not grant permission to use the trade
      names, trademarks, service marks, or product names of the Licensor,
      except as required for reasonable and customary use in describing the
      origin of the Work and reproducing the content of the NOTICE file.

   7. Disclaimer of Warranty. Unless required by applicable law or
      agreed to in writing, Licensor provides the Work (and each
      Contributor provides its Contributions) on an "AS IS" BASIS,
      WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
      implied, including, without limitation, any warranties or conditions
      of TITLE, NON-INFRINGEMENT, MERCHANTABILITY, or FITNESS FOR A
      PARTICULAR PURPOSE. You are solely responsible for determining the
      appropriateness of using or redistributing the Work and assume any
      risks associated with Your exercise of permissions under this License.

   8. Limitation of Liability. In no event and under no legal theory,
      whether in tort (including negligence), contract, or otherwise,
      unless required by applicable law (such as deliberate and grossly
      negligent acts) or agreed to in writing, shall any Contributor be
      liable to You for damages, including any direct, indirect, special,
      incidental, or consequential damages of any character arising as a
      result of this License or out of the use or inability to use the
      Work (including but not limited to damages for loss of goodwill,
      work stoppage, computer failure or malfunction, or any and all
      other commercial damages or losses), even if such Contributor
      has been advised of the possibility of such damages.

   9. Accepting Warranty or Additional Liability. While redistributing
      the Work or Derivative Works thereof, You may choose to offer,
      and charge a fee for, acceptance of support, warranty, indemnity,
      or other liability obligations and/or rights consistent with this
      License. However, in accepting such obligations, You may act only
      on Your own behalf and on Your sole responsibility, not on behalf
      of any other Contributor, and only if You agree to indemnify,
      defend, and hold each Contributor harmless for any liability
      incurred by, or claims asserted against, such Contributor by reason
      of your accepting any such warranty or additional liability.

   END OF TERMS AND CONDITIONS

   APPENDIX: How to apply the Apache License to your work.

      To apply the Apache License to your work, attach the following
      boilerplate notice, with the fields enclosed by brackets "[]"
      replaced with your own identifying information. (Don't include
      the brackets!)  The text should be enclosed in the appropriate
      comment syntax for the file format. We also recommend that a
      file or class name and description of purpose be included on the
      same "printed page" as the copyright notice for easier
      identification within third-party archives.

   Copyright [yyyy] [name of copyright owner]

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
```

### SIL Open Font License, Version 1.1

Applies to the bundled fonts: JetBrains Mono, Space Grotesk (npm packages: @fontsource-variable/jetbrains-mono, @fontsource-variable/space-grotesk).

```text
-----------------------------------------------------------
SIL OPEN FONT LICENSE Version 1.1 - 26 February 2007
-----------------------------------------------------------

PREAMBLE
The goals of the Open Font License (OFL) are to stimulate worldwide
development of collaborative font projects, to support the font creation
efforts of academic and linguistic communities, and to provide a free and
open framework in which fonts may be shared and improved in partnership
with others.

The OFL allows the licensed fonts to be used, studied, modified and
redistributed freely as long as they are not sold by themselves. The
fonts, including any derivative works, can be bundled, embedded,
redistributed and/or sold with any software provided that any reserved
names are not used by derivative works. The fonts and derivatives,
however, cannot be released under any other type of license. The
requirement for fonts to remain under this license does not apply
to any document created using the fonts or their derivatives.

DEFINITIONS
"Font Software" refers to the set of files released by the Copyright
Holder(s) under this license and clearly marked as such. This may
include source files, build scripts and documentation.

"Reserved Font Name" refers to any names specified as such after the
copyright statement(s).

"Original Version" refers to the collection of Font Software components as
distributed by the Copyright Holder(s).

"Modified Version" refers to any derivative made by adding to, deleting,
or substituting -- in part or in whole -- any of the components of the
Original Version, by changing formats or by porting the Font Software to a
new environment.

"Author" refers to any designer, engineer, programmer, technical
writer or other person who contributed to the Font Software.

PERMISSION & CONDITIONS
Permission is hereby granted, free of charge, to any person obtaining
a copy of the Font Software, to use, study, copy, merge, embed, modify,
redistribute, and sell modified and unmodified copies of the Font
Software, subject to the following conditions:

1) Neither the Font Software nor any of its individual components,
in Original or Modified Versions, may be sold by itself.

2) Original or Modified Versions of the Font Software may be bundled,
redistributed and/or sold with any software, provided that each copy
contains the above copyright notice and this license. These can be
included either as stand-alone text files, human-readable headers or
in the appropriate machine-readable metadata fields within text or
binary files as long as those fields can be easily viewed by the user.

3) No Modified Version of the Font Software may use the Reserved Font
Name(s) unless explicit written permission is granted by the corresponding
Copyright Holder. This restriction only applies to the primary font name as
presented to the users.

4) The name(s) of the Copyright Holder(s) or the Author(s) of the Font
Software shall not be used to promote, endorse or advertise any
Modified Version, except to acknowledge the contribution(s) of the
Copyright Holder(s) and the Author(s) or with their explicit written
permission.

5) The Font Software, modified or unmodified, in part or in whole,
must be distributed entirely under this license, and must not be
distributed under any other license. The requirement for fonts to
remain under this license does not apply to any document created
using the Font Software.

TERMINATION
This license becomes null and void if any of the above conditions are
not met.

DISCLAIMER
THE FONT SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO ANY WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT
OF COPYRIGHT, PATENT, TRADEMARK, OR OTHER RIGHT. IN NO EVENT SHALL THE
COPYRIGHT HOLDER BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY,
INCLUDING ANY GENERAL, SPECIAL, INDIRECT, INCIDENTAL, OR CONSEQUENTIAL
DAMAGES, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
FROM, OUT OF THE USE OR INABILITY TO USE THE FONT SOFTWARE OR FROM
OTHER DEALINGS IN THE FONT SOFTWARE.
```
