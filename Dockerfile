FROM scratch

COPY ./bin/evo /bin/evo
ENTRYPOINT [ "/bin/evo", "/migrations" ]