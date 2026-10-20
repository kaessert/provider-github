FROM gcr.io/distroless/static@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087

ARG TARGETOS
ARG TARGETARCH

ADD bin/$TARGETOS\_$TARGETARCH/provider /usr/local/bin/crossplane-github-provider

USER 65532
ENTRYPOINT ["crossplane-github-provider"]
